package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// timeParams are the query parameters the dashboard fills from its clock. They
// are keyed as minutes from the recording moment, so a replay whose clock is
// frozen there finds them again.
var timeParams = map[string]bool{"since": true, "until": true, "from": true, "to": true, "headline_since": true, "headline_until": true}

// recordKey is how a request is looked up on replay: method, path and the query
// with empty values dropped, keys sorted, tz ignored and clock times relative.
// web/lib/demo/replay-key.ts must spell it the same way (testdata/replay-keys.json).
func recordKey(method, path string, q url.Values, now time.Time) string {
	keys := make([]string, 0, len(q))
	for k := range q {
		if k == "tz" || q.Get(k) == "" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		v := q.Get(k)
		if timeParams[k] {
			if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
				v = fmt.Sprintf("@%dm", int(math.Round(t.Sub(now).Minutes())))
			}
		}
		parts = append(parts, k+"="+v)
	}
	return method + " " + path + "?" + strings.Join(parts, "&")
}

var idSegment = regexp.MustCompile(`^([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}|[0-9]+)$`)

// shardOf names the manifest file that holds a path's recordings: the path with
// every id segment (a UUID or a number) replaced by "_", so each endpoint is one
// file the replay loads the first time it is asked for.
// web/lib/demo/replay-key.ts must spell it the same way (testdata/replay-shards.json).
func shardOf(path string) string {
	segs := strings.Split(strings.Trim(path, "/"), "/")
	for i, s := range segs {
		if idSegment.MatchString(s) {
			segs[i] = "_"
		}
	}
	return strings.Join(segs, ".") + ".json"
}

type entry struct {
	Key     string            `json:"key"`
	Status  int               `json:"status"`
	Body    string            `json:"body"` // content hash, file under bodies/
	Headers map[string]string `json:"headers,omitempty"`
}

type recorder struct {
	api     *api
	db      *pgx.Conn // the weekly AI report reads the week's segments (aireport.go)
	now     time.Time
	out     string
	entries map[string]entry
	shard   map[string]string // key -> shard file
	bodies  map[string]int
	failed  int
}

func (rc *recorder) get(path string, q url.Values) []byte {
	if q == nil {
		q = url.Values{}
	}
	key := recordKey("GET", path, q, rc.now)
	if e, ok := rc.entries[key]; ok {
		data, _ := os.ReadFile(filepath.Join(rc.out, "bodies", e.Body+".json"))
		return data
	}
	u := rc.api.base + path
	if enc := q.Encode(); enc != "" {
		u += "?" + enc
	}
	req, _ := http.NewRequest("GET", u, nil)
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	resp, err := rc.api.client.Do(req)
	if err != nil {
		log.Printf("GET %s: %v", u, err)
		rc.failed++
		return nil
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 && resp.StatusCode != http.StatusNotFound {
		rc.failed++
		log.Printf("GET %s: %d %s", u, resp.StatusCode, strings.TrimSpace(string(data[:min(len(data), 200)])))
	}
	e := entry{Key: key, Status: resp.StatusCode, Body: rc.keep(data)}
	if v := resp.Header.Get("X-Session-Start-Offset"); v != "" {
		e.Headers = map[string]string{"X-Session-Start-Offset": v}
	}
	rc.entries[key] = e
	rc.shard[key] = shardOf(path)
	return data
}

// keep writes a body once under its content hash and returns the hash.
func (rc *recorder) keep(data []byte) string {
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:12])
	if _, ok := rc.bodies[hash]; !ok {
		rc.bodies[hash] = len(data)
		_ = writeFile(filepath.Join(rc.out, "bodies", hash+".json"), data)
	}
	return hash
}

// replace swaps a recorded answer's body for one the recorder wrote. Only the
// weekly AI report is written this way (aireport.go).
func (rc *recorder) replace(path string, q url.Values, data []byte) {
	key := recordKey("GET", path, q, rc.now)
	e := rc.entries[key]
	e.Body = rc.keep(data)
	rc.entries[key] = e
}

func (rc *recorder) iso(d time.Duration) string { return rc.now.Add(d).UTC().Format(time.RFC3339Nano) }

func q(kv ...string) url.Values {
	v := url.Values{}
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i+1] != "" {
			v.Set(kv[i], kv[i+1])
		}
	}
	return v
}

func decode[T any](data []byte) T {
	var v T
	_ = json.Unmarshal(data, &v)
	return v
}

func runRecord(args []string) {
	fs := flag.NewFlagSet("record", flag.ExitOnError)
	server := fs.String("server", "http://127.0.0.1:8080", "dashboard base URL")
	out := fs.String("out", "demo-fixtures", "output directory")
	nowFlag := fs.String("now", "", "the demo's frozen moment, RFC3339 (default: now); data after it does not exist on screen")
	days := fs.Int("days", 35, "days of data, for how far back the previous-window recordings reach")
	_ = fs.Parse(args)
	email, password, dbURL := os.Getenv("DEMO_ADMIN_EMAIL"), os.Getenv("DEMO_ADMIN_PASSWORD"), os.Getenv("DEMO_DATABASE_URL")
	if dbURL == "" {
		log.Fatal("DEMO_DATABASE_URL is required: the usage facts are read from the database")
	}
	a := newAPI(*server)
	if err := a.do("POST", "/api/auth/login", map[string]string{"email": email, "password": password}, "", nil); err != nil {
		log.Fatalf("login: %v", err)
	}
	now := time.Now().Truncate(time.Minute)
	if *nowFlag != "" {
		t, err := time.Parse(time.RFC3339, *nowFlag)
		if err != nil {
			log.Fatalf("-now: %v", err)
		}
		now = t
	}
	status := func() ([]byte, error) {
		var raw json.RawMessage
		err := a.do("GET", "/api/maintenance/status", nil, "", &raw)
		return raw, err
	}
	if err := waitForAnalysis(status, 10*time.Second, 30*time.Minute); err != nil {
		log.Fatal(err)
	}
	db, err := pgx.Connect(context.Background(), dbURL)
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer db.Close(context.Background())
	rc := &recorder{api: a, db: db, now: now.UTC(), out: *out, entries: map[string]entry{}, shard: map[string]string{}, bodies: map[string]int{}}
	started := time.Now()
	sections := []struct {
		name string
		run  func(*recorder)
	}{
		{"shell", recordShell}, {"trends", recordTrends}, {"history", func(rc *recorder) { recordHistory(rc, *days) }},
		{"usage", recordUsage}, {"sessions", recordSessions}, {"weekly", recordWeekly},
		{"inventory", recordInventory}, {"system", recordSystem},
	}
	for _, s := range sections {
		before, bytesBefore := len(rc.entries), rc.totalBytes()
		s.run(rc)
		log.Printf("%-10s %6d requests %8.1f MB new bodies", s.name, len(rc.entries)-before, float64(rc.totalBytes()-bytesBefore)/1e6)
	}
	moved, err := rc.moveServerStamps()
	if err != nil {
		log.Fatalf("server stamps: %v", err)
	}
	log.Printf("server stamps: %d moved before the demo's moment", moved)
	if err := writeFacts(context.Background(), dbURL, filepath.Join(rc.out, "facts.json")); err != nil {
		log.Fatalf("facts: %v", err)
	}
	if err := rc.writeManifest(); err != nil {
		log.Fatalf("manifest: %v", err)
	}
	log.Printf("%d requests, %d distinct bodies, %.1f MB raw, %.1f MB gzipped, %d failed, %s",
		len(rc.entries), len(rc.bodies), float64(rc.totalBytes())/1e6, float64(rc.gzippedBytes())/1e6, rc.failed, time.Since(started).Round(time.Second))
}

// waitForAnalysis returns once the server has analysed every session: until
// then the session lists and traces it answers are half built, and a recording
// would keep them that way.
func waitForAnalysis(status func() ([]byte, error), poll, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		var s struct {
			TokenRulePending bool `json:"token_rule_pending"`
			Analysis         struct {
				Enabled           bool `json:"enabled"`
				WaitingRequestSet bool `json:"waiting_for_request_set"`
				WaitingCommits    bool `json:"waiting_for_commit_claims"`
				Build             struct {
					State string `json:"state"`
				} `json:"build"`
				Queue struct {
					Depth int `json:"depth"`
				} `json:"queue"`
			} `json:"session_analysis"`
		}
		raw, err := status()
		if err == nil {
			err = json.Unmarshal(raw, &s)
		}
		a := s.Analysis
		if err == nil && !s.TokenRulePending && (!a.Enabled ||
			(!a.WaitingRequestSet && !a.WaitingCommits && a.Build.State == "done" && a.Queue.Depth == 0)) {
			return nil
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("session analysis not settled after %s (last status: %s, %v)", timeout, raw, err)
		}
		log.Printf("session analysis: build %s, %d queued, waiting", a.Build.State, a.Queue.Depth)
		time.Sleep(poll)
	}
}

var stampRE = regexp.MustCompile(`"([A-Za-z_]+)":"(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2}))"`)

// futureKeys name times that are ahead of any present by meaning: a window's
// reset, the next scheduled run, an expiry, the end of a range.
var futureKeys = map[string]bool{"resets_at": true, "next_run": true, "expires_at": true, "until": true, "to": true}

// moveServerStamps mocks the server's clock in the recordings. The data ends at
// the demo's moment, but the server stamps what it does itself -- an account
// created, a client's last sync, a rule first seen -- with its own clock, hours
// or days after that moment by the time the recorder runs. Every such stamp is
// moved back by one offset, so the latest lands a minute before the moment and
// their order is kept.
func (rc *recorder) moveServerStamps() (int, error) {
	late := func(data []byte, each func(key string, t time.Time)) {
		for _, m := range stampRE.FindAllSubmatch(data, -1) {
			if futureKeys[string(m[1])] {
				continue
			}
			if t, err := time.Parse(time.RFC3339Nano, string(m[2])); err == nil && t.After(rc.now) {
				each(string(m[1]), t)
			}
		}
	}
	read := func(hash string) ([]byte, error) { return os.ReadFile(filepath.Join(rc.out, "bodies", hash+".json")) }
	var latest time.Time
	for hash := range rc.bodies {
		data, err := read(hash)
		if err != nil {
			return 0, err
		}
		late(data, func(_ string, t time.Time) {
			if t.After(latest) {
				latest = t
			}
		})
	}
	if latest.IsZero() {
		return 0, nil
	}
	shift := latest.Sub(rc.now) + time.Minute
	moved, used := 0, map[string]bool{}
	for key, e := range rc.entries {
		data, err := read(e.Body)
		if err != nil {
			return 0, err
		}
		out := stampRE.ReplaceAllFunc(data, func(m []byte) []byte {
			sm := stampRE.FindSubmatch(m)
			t, err := time.Parse(time.RFC3339Nano, string(sm[2]))
			if futureKeys[string(sm[1])] || err != nil || !t.After(rc.now) {
				return m
			}
			moved++
			return []byte(`"` + string(sm[1]) + `":"` + t.Add(-shift).UTC().Format(time.RFC3339Nano) + `"`)
		})
		if !bytes.Equal(out, data) {
			e.Body = rc.keep(out)
			rc.entries[key] = e
		}
		used[e.Body] = true
	}
	for hash := range rc.bodies {
		if !used[hash] {
			delete(rc.bodies, hash)
			if err := os.Remove(filepath.Join(rc.out, "bodies", hash+".json")); err != nil {
				return 0, err
			}
		}
	}
	return moved, nil
}

// writeManifest writes manifest.json (the moment and the shard list) and one
// file per endpoint under manifest/.
func (rc *recorder) writeManifest() error {
	shards := map[string][]entry{}
	for key, e := range rc.entries {
		shards[rc.shard[key]] = append(shards[rc.shard[key]], e)
	}
	names := make([]string, 0, len(shards))
	for name, list := range shards {
		sort.Slice(list, func(i, j int) bool { return list[i].Key < list[j].Key })
		if err := writeJSON(filepath.Join(rc.out, "manifest", name), list); err != nil {
			return err
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return writeJSON(filepath.Join(rc.out, "manifest.json"), map[string]any{"recorded_at": rc.now.Format(time.RFC3339), "shards": names})
}

func (rc *recorder) totalBytes() int {
	n := 0
	for _, b := range rc.bodies {
		n += b
	}
	return n
}

func (rc *recorder) gzippedBytes() int {
	n := 0
	for hash := range rc.bodies {
		data, _ := os.ReadFile(filepath.Join(rc.out, "bodies", hash+".json"))
		var buf bytes.Buffer
		w, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
		_, _ = w.Write(data)
		_ = w.Close()
		n += buf.Len()
	}
	return n
}

// facts are the rows of visible_events, the view every trend and cost total is
// summed from. The demo computes a window nobody recorded -- a zoom, a step
// back, a filter combination -- from these, with the server's own grouping
// (web/lib/demo/compute.ts). Strings are stored once and referenced by index;
// -1 is NULL.
type facts struct {
	Strings []string  `json:"strings"`
	TS      []int64   `json:"ts"` // microseconds since the epoch, the database's own precision
	Model   []int     `json:"model"`
	UserID  []int     `json:"user_id"`
	Profile []int     `json:"profile_email"`
	Login   []int     `json:"login_email"`
	Team    []int     `json:"user_team"`
	Agent   []int     `json:"agent"`
	Billing []int     `json:"billing_provider"`
	Cost    []float64 `json:"cost_usd"`
	Input   []int64   `json:"input_tokens"`
	Output  []int64   `json:"output_tokens"`
}

func writeFacts(ctx context.Context, dbURL, path string) error {
	db, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		return err
	}
	defer db.Close(ctx)
	rows, err := db.Query(ctx, `SELECT (extract(epoch FROM ts) * 1000000)::bigint, model, user_id, profile_email, login_email,
		user_team, agent, billing_provider, cost_usd, input_tokens, output_tokens
		FROM visible_events ORDER BY ts`)
	if err != nil {
		return err
	}
	defer rows.Close()
	f := facts{}
	index := map[string]int{}
	str := func(s *string) int {
		if s == nil {
			return -1
		}
		i, ok := index[*s]
		if !ok {
			i = len(f.Strings)
			index[*s] = i
			f.Strings = append(f.Strings, *s)
		}
		return i
	}
	for rows.Next() {
		var ts int64
		var model, user, profile, login, team, agent, billing *string
		var cost *float64
		var in, out *int64
		if err := rows.Scan(&ts, &model, &user, &profile, &login, &team, &agent, &billing, &cost, &in, &out); err != nil {
			return err
		}
		f.TS = append(f.TS, ts)
		f.Model = append(f.Model, str(model))
		f.UserID = append(f.UserID, str(user))
		f.Profile = append(f.Profile, str(profile))
		f.Login = append(f.Login, str(login))
		f.Team = append(f.Team, str(team))
		f.Agent = append(f.Agent, str(agent))
		f.Billing = append(f.Billing, str(billing))
		f.Cost = append(f.Cost, deref(cost))
		f.Input = append(f.Input, deref(in))
		f.Output = append(f.Output, deref(out))
	}
	if err := rows.Err(); err != nil {
		return err
	}
	log.Printf("facts: %d rows", len(f.TS))
	return writeJSON(path, f)
}

func deref[T int64 | float64](p *T) T {
	if p == nil {
		return 0
	}
	return *p
}

var (
	agents     = []string{"", "claude", "codex", "other", "weekly"}
	categories = []string{"", "anthropic", "codex", "compatible"}
	sources    = []string{"interactive", "headless", "all"}
	windows    = []struct {
		gran string
		d    time.Duration
	}{{"minute", 3 * time.Hour}, {"hour", 72 * time.Hour}, {"day", 30 * 24 * time.Hour}, {"week", 26 * 7 * 24 * time.Hour}, {"month", 365 * 24 * time.Hour}}
)

func (rc *recorder) accounts() []string {
	return append([]string{""}, decode[[]string](rc.get("/api/accounts", nil))...)
}

func recordShell(rc *recorder) {
	for _, p := range []string{"/api/auth/setup-status", "/api/auth/me", "/api/maintenance/status", "/api/version", "/api/me/quota-usage", "/api/users/names", "/api/deletion-policy"} {
		rc.get(p, nil)
	}
	rc.accounts()
}

// recordTrends records the trend chart's opening view under one filter at a
// time. Other combinations, and windows the chart moves to, are computed from
// the usage facts on replay; these recordings are also what that computation is
// checked against.
func recordTrends(rc *recorder) {
	type combo struct{ account, agent, category, model, user string }
	var combos []combo
	for _, acct := range rc.accounts() {
		combos = append(combos, combo{account: acct})
	}
	for _, ag := range agents[1:] {
		combos = append(combos, combo{agent: ag})
	}
	for _, c := range categories[1:] {
		combos = append(combos, combo{category: c})
	}
	models := decode[[]struct {
		Model string `json:"model"`
	}](rc.get("/api/cost/by-model", q("since", rc.iso(-30*24*time.Hour))))
	seen := map[string]bool{}
	for _, m := range models {
		if !seen[m.Model] {
			seen[m.Model] = true
			combos = append(combos, combo{model: m.Model})
		}
	}
	for _, u := range rc.users() {
		combos = append(combos, combo{user: u.UserID})
	}
	for _, c := range combos {
		rc.get("/api/stats/latest-activity", q("login_email", c.account, "user_id", c.user, "agent", c.agent, "model_category", c.category, "model", c.model))
		for _, w := range windows {
			since := rc.iso(-w.d)
			rc.get("/api/stats/timeseries-by-user", q("granularity", w.gran, "since", since, "login_email", c.account, "tz", "Asia/Seoul", "model_category", c.category, "model", c.model, "agent", c.agent))
			rc.get("/api/stats/timeseries-by-model", q("granularity", w.gran, "since", since, "login_email", c.account, "tz", "Asia/Seoul", "user_id", c.user, "agent", c.agent, "model_category", c.category))
			if c.agent == "" && c.category != "compatible" && c.model == "" && c.user == "" {
				rc.coverage(since, rc.iso(0), w.gran, w.d, c.account)
			}
			if c.agent == "" && c.category == "" && c.model == "" {
				rc.get("/api/cost/by-user", q("since", since, "login_email", c.account))
				rc.get("/api/cost/by-model", q("since", since, "login_email", c.account, "user_id", c.user))
			}
		}
	}
	// Every reading, once: the replay slices any window the chart asks for out of
	// this, as the server would.
	rc.get("/api/quota-samples", q("from", "1970-01-01T00:00:00Z"))
	for _, w := range windows {
		rc.get("/api/quota-samples", q("from", rc.iso(-w.d)))
	}
}

// coverage records the coverage overlay the way the chart asks for it: the
// headline reads the trailing week when the window is shorter than a day.
func (rc *recorder) coverage(since, until, gran string, window time.Duration, account string) {
	head := since
	if window < 24*time.Hour {
		u, _ := time.Parse(time.RFC3339Nano, until)
		head = u.Add(-7 * 24 * time.Hour).UTC().Format(time.RFC3339Nano)
	}
	rc.get("/api/stats/coverage-gap", q("since", since, "until", until, "login_email", account, "granularity", gran,
		"headline_since", head, "headline_until", until, "tz", "Asia/Seoul"))
}

// recordHistory records the coverage overlay for the windows the chart steps
// back to under its default filters, across the whole data. Coverage is the one
// trend number the replay cannot compute: it is a fit over the plan meters.
func recordHistory(rc *recorder, days int) {
	start := rc.now.Add(-time.Duration(days+1) * 24 * time.Hour)
	for _, w := range windows {
		for k := 1; ; k++ {
			until := rc.now.Add(-time.Duration(k) * w.d)
			if until.Before(start) {
				break
			}
			rc.coverage(until.Add(-w.d).UTC().Format(time.RFC3339Nano), until.UTC().Format(time.RFC3339Nano), w.gran, w.d, "")
		}
	}
}

type userRow struct {
	ProfileEmail string `json:"profile_email"`
	UserID       string `json:"user_id"`
}

func (rc *recorder) users() []userRow {
	var out []userRow
	seen := map[string]bool{}
	for _, u := range decode[[]userRow](rc.get("/api/cost/by-user", q("since", rc.iso(-30*24*time.Hour)))) {
		if u.UserID != "" && !seen[u.UserID] {
			seen[u.UserID] = true
			out = append(out, u)
		}
	}
	return out
}

func recordUsage(rc *recorder) {
	for _, u := range rc.users() {
		for _, w := range append(windows, struct {
			gran string
			d    time.Duration
		}{"", 30 * 24 * time.Hour}) {
			since := rc.iso(-w.d)
			rc.get("/api/session-overview", q("profile_email", u.ProfileEmail, "since", since, "limit", "5", "offset", "0"))
			rc.get("/api/stats/latest-activity", q("profile_email", u.ProfileEmail, "user_id", u.UserID))
		}
	}
	rc.get("/api/admin/users", nil)
	rc.get("/api/admin/otel-user-ids", nil)
	rc.get("/api/users", nil)
}

func recordSessions(rc *recorder) {
	type row struct {
		SessionID    string `json:"session_id"`
		AccountCount int    `json:"account_count"`
	}
	all := map[string]row{}
	list := func(extra url.Values) {
		for off := 0; ; off += 100 {
			params := q("limit", "100", "assembled", "1", "offset", fmt.Sprint(off))
			for k, v := range extra {
				params[k] = v
			}
			p := decode[[]row](rc.get("/api/session-overview", params))
			for _, s := range p {
				all[s.SessionID] = s
			}
			if len(p) < 100 {
				return
			}
		}
	}
	scope := func(src, agent, account string) url.Values {
		return q("source", src, "agent", agent, "login_email", account, "assembled", "1")
	}
	for _, src := range sources {
		for _, ag := range agents {
			for _, acct := range rc.accounts() {
				s := scope(src, ag, acct)
				rc.get("/api/projects", s)
				rc.get("/api/me/session-groups", s)
				list(s)
				cnt := scope(src, ag, acct)
				cnt.Set("include_classification_counts", "0")
				rc.get("/api/session-overview/count", cnt)
				cnt.Set("source", "all")
				rc.get("/api/session-overview/count", cnt)
				cnt = scope(src, ag, acct)
				cnt.Set("include_classification_counts", "1")
				rc.get("/api/session-overview/count", cnt)
				for _, sort := range []string{"start", "activity", "cost", "requests"} {
					a := scope(src, ag, acct)
					a.Set("include", "analysis")
					a.Set("sort", sort)
					list(a)
					rc.get("/api/me/session-groups", a)
					rc.get("/api/session-overview/count", a)
					everywhere := scope("all", ag, acct)
					everywhere.Set("include", "analysis")
					everywhere.Set("sort", sort)
					rc.get("/api/session-overview/count", everywhere)
				}
			}
		}
	}
	log.Printf("sessions: %d distinct in the lists", len(all))
	for id, s := range all {
		// The replay answers a session opened under an account scope with its
		// unscoped recording (web/lib/demo/replayer.ts), which is the same answer
		// only for a session that ran under one account.
		if s.AccountCount > 1 {
			log.Fatalf("session %s ran under %d accounts; the demo replays session detail unscoped", id, s.AccountCount)
		}
		sp := "/api/sessions/" + id
		rc.get(sp+"/analysis", q("view", "repo-status"))
		rc.get(sp+"/trace/index", nil)
		rc.get(sp+"/conversation/timeline", nil)
		rc.get(sp+"/personal", nil)
		rc.get("/api/sessions", q("session_id", id, "lineage", "1", "view", "conversation", "raw", "0", "order", "asc", "limit", "1000", "offset", "0", "start", "latest-request"))
		rc.get("/api/sessions", q("session_id", id, "limit", "1000", "offset", "0", "order", "asc", "lineage", "1", "raw", "0", "view", "conversation"))
		roots := decode[struct {
			Nodes []struct {
				NodeID      string `json:"node_id"`
				HasChildren bool   `json:"has_children"`
			} `json:"nodes"`
		}](rc.get(sp+"/trace/nodes", q("view", "flow", "limit", "20")))
		for _, n := range roots.Nodes {
			if n.HasChildren {
				rc.get(sp+"/trace/nodes", q("view", "flow", "limit", "20", "parent_id", n.NodeID))
			}
		}
	}
	for wk := 0; wk < 6; wk++ {
		from := mondayKST(rc.now).AddDate(0, 0, -7*wk)
		span := q("from", from.UTC().Format(time.RFC3339Nano), "to", from.AddDate(0, 0, 7).UTC().Format(time.RFC3339Nano))
		for _, src := range sources {
			for _, ag := range []string{"", "claude", "codex", "other"} {
				for _, acct := range rc.accounts() {
					c := scope(src, ag, acct)
					c.Set("from", span.Get("from"))
					c.Set("to", span.Get("to"))
					rc.get("/api/session-calendar", c)
				}
			}
		}
		for _, u := range rc.users() {
			c := scope("interactive", "", "")
			c.Set("from", span.Get("from"))
			c.Set("to", span.Get("to"))
			c.Set("user_id", u.UserID)
			rc.get("/api/session-calendar", c)
		}
	}
}

func mondayKST(t time.Time) time.Time {
	k := t.In(kst)
	d := time.Date(k.Year(), k.Month(), k.Day(), 0, 0, 0, 0, kst)
	return d.AddDate(0, 0, -((int(d.Weekday()) + 6) % 7))
}

func recordWeekly(rc *recorder) {
	rc.get("/api/projects", nil)
	for wk := 0; wk <= 6; wk++ {
		start := mondayKST(rc.now).AddDate(0, 0, -7*wk)
		since, until := start.UTC().Format(time.RFC3339Nano), start.AddDate(0, 0, 7).UTC().Format(time.RFC3339Nano)
		ins := decode[struct {
			Tasks []struct {
				TaskType string `json:"task_type"`
			} `json:"tasks"`
		}](rc.get("/api/weekly-insights", q("since", since, "until", until, "tz", "Asia/Seoul")))
		y, w := start.ISOWeek()
		week := q("week", fmt.Sprintf("%d-W%02d", y, w), "tz", "Asia/Seoul")
		segs, err := weekSegments(context.Background(), rc.db, viewer(), start, start.AddDate(0, 0, 7))
		if err != nil {
			log.Fatalf("week %s segments: %v", week.Get("week"), err)
		}
		rc.replace("/api/ai-reports", week, writtenAIReport(rc.get("/api/ai-reports", week), segs, rc.now))
		for _, t := range ins.Tasks {
			if t.TaskType != "non-task" && t.TaskType != "unknown" {
				rc.get("/api/task-segments", q("since", since, "until", until, "task_type", t.TaskType))
			}
		}
	}
}

func recordInventory(rc *recorder) {
	for _, acct := range rc.accounts() {
		tools := decode[[]struct {
			ToolName string `json:"tool_name"`
		}](rc.get("/api/tools", q("since", rc.iso(-30*24*time.Hour), "login_email", acct)))
		for _, t := range tools {
			rc.get("/api/tools/detail", q("tool_name", t.ToolName, "since", rc.iso(-24*time.Hour), "login_email", acct, "granularity", "minute", "limit", "50", "tz", "Asia/Seoul"))
			rc.get("/api/tools/detail", q("tool_name", t.ToolName, "since", rc.iso(-30*24*time.Hour), "login_email", acct, "granularity", "day", "tz", "Asia/Seoul"))
		}
		for _, days := range []int{7, 30, 90, 0} {
			since := "1970-01-01T00:00:00.000Z"
			if days > 0 {
				since = rc.iso(-time.Duration(days) * 24 * time.Hour)
			}
			for _, ag := range []string{"", "claude", "codex"} {
				rc.get("/api/plugins", q("since", since, "login_email", acct, "agent", ag))
				if ag != "claude" {
					rc.get("/api/skills", q("since", since, "login_email", acct, "agent", ag))
				}
			}
		}
	}
	type rule struct {
		ID int64 `json:"id"`
	}
	for _, status := range []string{"all", "active", "missing", "deleted", "unreadable", "archived"} {
		for _, ag := range []string{"", "claude", "codex"} {
			data := rc.get("/api/project-rules", q("agent", ag, "status", status, "limit", "200"))
			if status == "all" && ag == "" {
				var list struct {
					Items []rule `json:"items"`
				}
				_ = json.Unmarshal(data, &list)
				for _, r := range list.Items {
					rc.get(fmt.Sprintf("/api/project-rules/%d", r.ID), nil)
				}
			}
		}
	}
	for _, acct := range rc.accounts() {
		for page := 0; page < 5; page++ {
			off := ""
			if page > 0 {
				off = fmt.Sprint(page * 50)
			}
			rc.get("/api/events", q("login_email", acct, "since", rc.iso(-30*24*time.Hour), "limit", "50", "offset", off))
			rc.get("/api/metrics", q("profile_email", acct, "since", rc.iso(-30*24*time.Hour), "limit", "50", "offset", off))
		}
	}
}

func recordSystem(rc *recorder) {
	for _, p := range []string{"/api/admin/client-versions", "/api/admin/retention", "/api/admin/excluded-accounts",
		"/api/admin/excluded-billing-accounts", "/api/admin/unpriced-models", "/api/admin/ai", "/api/auth/api-tokens",
		"/api/self-exclusions/billing-accounts", "/api/me/quota-bindings", "/api/blocked-projects", "/api/projects"} {
		rc.get(p, nil)
	}
	rc.get("/api/admin/quota-bindings", q("status", "pending"))
	rc.get("/api/admin/quota-bindings", q("status", "approved"))
	rc.get("/api/admin/organization-insights", q("since", rc.iso(-7*24*time.Hour), "until", rc.iso(0)))
}

// viewer is the persona the demo is signed in as: the team's admin.
func viewer() *persona {
	for _, p := range team {
		if p.Admin {
			return p
		}
	}
	panic("the team has no admin")
}
