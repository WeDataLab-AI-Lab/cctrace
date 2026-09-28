package codexlog

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"time"

	"cctrace/internal/jsonlscan"
)

// Codex needs no quota poller. Every turn's response carries the account's
// rate-limit state back in the HTTP headers, the CLI records the result in the
// session JSONL, and cctrace already synchronises those files. The reading has
// been sitting in data on disk the whole time — the parser simply had no field
// declared for it, so it was unmarshalled into nothing.
//
// One consequence is worth stating: unlike Claude, whose usage exists only at
// the moment it is polled, Codex history can be reconstructed backwards from
// files that already exist.

// RateLimitWindow is one bucket of a Codex rate-limit reading.
type RateLimitWindow struct {
	UsedPercent float64
	// WindowMinutes is the length of the window: 300 for the five-hour bucket,
	// 10080 for the weekly one. It is the identity of the window — the payload
	// positions (primary/secondary) do not fix which length appears where.
	WindowMinutes int
	ResetsAt      *time.Time
}

// A reading carries ONE limit_id, not the account's full set. Observed values
// are "codex" (overwhelmingly) and "premium" (which arrives with both windows
// null). The account meters more buckets than this — model-scoped ones live
// under limit ids the session log does not record, and reading those needs the
// app-server's rateLimitsByLimitId view. Whether the "codex" bucket itself has a
// five-hour window depends on the plan: Plus carries 300 beside 10080 (restored
// around 2026-08-25), Pro carries 10080 alone (removed 2026-07-12).
//
// RateLimitSample is one account-scoped reading taken at one instant.
//
// It is deliberately not a Record. Records become rows in session_records, and
// toStoreRecord forwards any non-empty record type straight through with no
// whitelist to stop it — so a reading modelled as a Record would silently land
// in a session-scoped table it does not belong to. This travels on its own
// channel instead.
type RateLimitSample struct {
	Timestamp time.Time
	PlanType  string // pro | prolite | "" when the payload did not say
	Windows   []RateLimitWindow

	// LimitID names the bucket these windows were metered against, and is empty
	// on payloads written before the field existed — both shapes are on disk.
	//
	// The reading carries ONE bucket, not the account's full set. The app-server
	// reports model-scoped buckets under other ids, so this file can never be the
	// whole picture. What the id is good for is telling the buckets apart:
	// several of them report a window of the same length, so the length alone
	// no longer identifies which meter a reading came off.
	LimitID string
	// LimitName is the server's display name for the bucket, when it gave one.
	LimitName string
}

// rawRateLimits is the payload.rate_limits object.
//
// Fields beyond these appear and disappear between Codex versions — credits,
// individual_limit, spend_control_reached, rate_limit_reached_type have all
// been observed on some readings and not others. Only what the chart needs is
// declared; the rest is ignored rather than guessed at.
type rawRateLimits struct {
	Primary   *rawRateLimitWindow `json:"primary"`
	Secondary *rawRateLimitWindow `json:"secondary"`
	PlanType  string              `json:"plan_type"`
	LimitID   string              `json:"limit_id"`
	LimitName string              `json:"limit_name"`
}

type rawRateLimitWindow struct {
	UsedPercent   float64 `json:"used_percent"`
	WindowMinutes int     `json:"window_minutes"`
	ResetsAt      int64   `json:"resets_at"` // unix epoch seconds, unlike Anthropic's ISO8601
}

// sample converts the payload, reporting false when it held no usable window.
//
// primary and secondary are read as an unordered pair. Observed readings put
// the weekly window in primary with secondary null, as well as the five-hour
// window in primary with weekly in secondary; filing by position would put
// weekly percentages on the five-hour line.
func (r *rawRateLimits) sample(ts time.Time) (RateLimitSample, bool) {
	s := RateLimitSample{Timestamp: ts, PlanType: r.PlanType, LimitID: r.LimitID, LimitName: r.LimitName}
	for _, w := range []*rawRateLimitWindow{r.Primary, r.Secondary} {
		// A window with no length cannot be filed against either series, and a
		// guess would place the reading on the wrong line.
		if w == nil || w.WindowMinutes <= 0 {
			continue
		}
		out := RateLimitWindow{UsedPercent: w.UsedPercent, WindowMinutes: w.WindowMinutes}
		if w.ResetsAt > 0 {
			t := time.Unix(w.ResetsAt, 0).UTC()
			out.ResetsAt = &t
		}
		s.Windows = append(s.Windows, out)
	}
	if len(s.Windows) == 0 {
		return RateLimitSample{}, false
	}
	return s, true
}

// ScanRateLimits reads only the rate-limit readings from a session file.
//
// ScanFileWithSamplesContext also builds every Record in the file, each holding
// the raw JSON line it came from. The backfill discards all of it, and the waste
// is measurable: on a 157MB session file the full path allocates 1,684MB to hand
// back 346 readings, where this one allocates 759MB for the identical result.
//
// Half, not a tenth — the remainder is the line buffer, which any pass over the
// file has to pay. What this removes is the Record set and the raw JSON each
// Record pins; what it holds is proportional to the answer, not to the input.
// Over the 3.0GB of session logs on the machine this was written against, the
// difference is several gigabytes of churn for data nobody reads.
func ScanRateLimits(ctx context.Context, path string) ([]RateLimitSample, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []RateLimitSample
	reader := bufio.NewReaderSize(f, 64*1024)
	for {
		lineBytes, consumed, complete, readErr := jsonlscan.ReadLine(ctx, reader)
		if errors.Is(readErr, jsonlscan.ErrLineTooLong) {
			log.Printf("[codexlog] %s: skipping oversized jsonl line (%d bytes)", path, consumed)
			continue
		}
		if readErr != nil && readErr != io.EOF {
			return out, readErr
		}
		if complete {
			line := bytes.TrimRight(lineBytes, "\r\n")
			// A cheap byte test before any parsing: the vast majority of lines
			// are messages and tool output, and unmarshalling those to discover
			// they hold no limits is the cost this function exists to avoid.
			if len(line) > 0 && bytes.Contains(line, rateLimitsKey) {
				if s, ok := rateLimitSampleFromLine(line); ok {
					out = append(out, s)
				}
			}
		}
		if readErr == io.EOF {
			break
		}
	}
	return out, nil
}

var rateLimitsKey = []byte(`"rate_limits"`)

// rateLimitSampleFromLine parses one line, declaring only the fields needed.
// Nothing else in the record is held.
func rateLimitSampleFromLine(line []byte) (RateLimitSample, bool) {
	var rl struct {
		Timestamp string `json:"timestamp"`
		Type      string `json:"type"`
		Payload   struct {
			Type       string         `json:"type"`
			RateLimits *rawRateLimits `json:"rate_limits"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(line, &rl); err != nil {
		return RateLimitSample{}, false
	}
	if rl.Type != "event_msg" || rl.Payload.Type != "token_count" || rl.Payload.RateLimits == nil {
		return RateLimitSample{}, false
	}
	ts, err := time.Parse(time.RFC3339Nano, rl.Timestamp)
	if err != nil {
		return RateLimitSample{}, false
	}
	return rl.Payload.RateLimits.sample(ts)
}
