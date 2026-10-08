package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cctrace/internal/syncer"
)

// The value column and the continuation indent are part of the contract: this
// line sits under `서버 도달:`, which reports a different subject, and a reader
// compares the two at a glance.
const (
	wantCollectionOK = "[OK] 전송 실패 기록이 없습니다"

	wantCollectionStalledWithDaemon = "[!] 데몬이 2일 3시간째 전송에 성공하지 못했습니다 (파일 312개 실패)\n" +
		"                       마지막 성공: 2026-09-16 15:22\n" +
		"                       최근 오류: dial tcp 10.20.30.40:18080: connect: no route to host\n" +
		"                       이 프로세스는 서버에 닿는데 데몬은 닿지 못한다면 데몬만 막혀 있습니다 --\n" +
		"                       cctrace sync --stop 뒤 cctrace sync --daemon 으로 다시 띄우면 회복됩니다"

	wantCollectionStalledNoDaemon = "[!] 전송에 성공하지 못한 채 2일 3시간이 지났고 데몬은 실행 중이 아닙니다 (파일 312개 실패)\n" +
		"                       마지막 성공: 2026-09-16 15:22\n" +
		"                       최근 오류: dial tcp 10.20.30.40:18080: connect: no route to host\n" +
		"                       cctrace sync --daemon 으로 다시 띄우거나, Claude Code 세션을 새로 열면 자동으로 뜹니다"

	wantCollectionServerRefusal = "[!] 서버가 1시간 12분째 요청 과다(429)로 전송을 거부하고 있습니다 (파일 4개 실패)\n" +
		"                       마지막 성공: 2026-09-16 15:22\n" +
		"                       최근 오류: send batch: server returned 429\n" +
		"                       재시작으로는 회복되지 않습니다 -- 서버의 요청 제한이 풀리기를 기다립니다. 오프셋은 그대로이므로 데이터는 사라지지 않습니다"
)

func transportStall(class string, elapsed time.Duration, filesFailed int, lastErr string) *syncer.TransportFailure {
	first := time.Date(2026, 9, 16, 15, 30, 0, 0, time.Local)
	return &syncer.TransportFailure{
		Class:         class,
		FirstFailedAt: first,
		LastFailedAt:  first.Add(elapsed),
		LastSuccessAt: time.Date(2026, 9, 16, 15, 22, 0, 0, time.Local),
		FilesFailed:   filesFailed,
		LastError:     lastErr,
	}
}

// #712: for two days `Sync status: [OK] healthy` was the only thing this command
// said while no byte left the machine. That line was true and answered a
// different question -- whether the status process itself can reach the server.
// This one answers whether the daemon is getting anything through, and the two
// are printed as separate subjects so "healthy" can never stand in for
// "collecting".
//
// The whole value is asserted, wording and layout, for the reason
// daemonRestartNotice is: the remedy is the part a person acts on.
func TestCollectionStatusValue(t *testing.T) {
	const dial = "dial tcp 10.20.30.40:18080: connect: no route to host"
	twoDaysThreeHours := 51 * time.Hour

	cases := []struct {
		name        string
		record      *syncer.TransportFailure
		daemonAlive bool
		want        string
	}{
		{
			name:        "no record is the ordinary case",
			record:      nil,
			daemonAlive: true,
			want:        wantCollectionOK,
		},
		{
			// A LAN blip, a Wi-Fi roam, a VPN toggle, a laptop waking up: seconds
			// to minutes. Reporting those is how a status line stops being read.
			name:        "a failure shorter than the threshold stays quiet",
			record:      transportStall("transport", 14*time.Minute, 3, dial),
			daemonAlive: true,
			want:        wantCollectionOK,
		},
		{
			name:        "a daemon that holds the lock and sends nothing",
			record:      transportStall("transport", twoDaysThreeHours, 312, dial),
			daemonAlive: true,
			want:        wantCollectionStalledWithDaemon,
		},
		{
			// After the daemon releases the lock and exits, the fact survives in
			// the state file. The remedy changes; the fact does not.
			name:        "the same stall with no daemon running",
			record:      transportStall("transport", twoDaysThreeHours, 312, dial),
			daemonAlive: false,
			want:        wantCollectionStalledNoDaemon,
		},
		{
			name:        "a server refusal says a restart will not help",
			record:      transportStall("server", 72*time.Minute, 4, "send batch: server returned 429"),
			daemonAlive: true,
			want:        wantCollectionServerRefusal,
		},
		{
			name:        "a record without a first failure stays quiet",
			record:      &syncer.TransportFailure{Class: "transport", LastFailedAt: time.Date(2026, 9, 18, 0, 0, 0, 0, time.Local), FilesFailed: 1},
			daemonAlive: true,
			want:        wantCollectionOK,
		},
		{
			// A clock that jumped forward and back is enough to write a record
			// whose last failure precedes its first. Reporting a negative stretch
			// is worse than saying nothing.
			name:        "a record whose timestamps run backwards stays quiet",
			record:      transportStall("transport", -time.Hour, 5, dial),
			daemonAlive: true,
			want:        wantCollectionOK,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := collectionStatusValue(tc.record, tc.daemonAlive)
			if got != tc.want {
				t.Errorf("collectionStatusValue =\n%s\nwant\n%s", got, tc.want)
			}
		})
	}
}

// The threshold is time, not passes: the installed hook polls once a second and
// the flag defaults to thirty, so a pass count means nothing in common between
// two installs.
func TestCollectionStallThresholdIsMeasuredInTime(t *testing.T) {
	just := transportStall("transport", collectionStallAfter, 1, "boom")
	if got := collectionStatusValue(just, true); got == wantCollectionOK {
		t.Errorf("a failure lasting exactly %s stayed quiet", collectionStallAfter)
	}
	under := transportStall("transport", collectionStallAfter-time.Second, 1, "boom")
	if got := collectionStatusValue(under, true); got != wantCollectionOK {
		t.Errorf("a failure one second under the threshold reported: %q", got)
	}
}

// The reachability line answers "can this process reach the server", and its
// wording has to say so. "[OK] healthy" read as a verdict on collection is the
// misreading #712 turned on.
func TestReachabilityWordingNamesTheProber(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/health" {
			t.Errorf("path = %q, want /api/health", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	got := checkHTTPHealth(srv.URL, "")
	if got != "[OK] 이 프로세스에서는 서버에 닿습니다" {
		t.Errorf("checkHTTPHealth = %q", got)
	}
	if strings.Contains(got, "healthy") {
		t.Errorf("wording still claims health rather than reachability: %q", got)
	}
}

// The exit waits long enough after the warning for a person to see it. The two
// are separate knobs, and the warning's clock runs on a record refreshed only
// every few minutes, so the gap between them is pinned rather than trusted.
func TestStallExitComesWellAfterTheWarning(t *testing.T) {
	if syncStallExitAfter < 2*collectionStallAfter {
		t.Errorf("exit after %s, warning after %s: the exit must wait at least twice as long",
			syncStallExitAfter, collectionStallAfter)
	}
}
