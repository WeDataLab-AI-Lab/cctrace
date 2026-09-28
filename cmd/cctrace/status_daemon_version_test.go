package main

import (
	"testing"
	"time"
)

// useDaemonLiveness pins whether a daemon holds the profile's sync lock, so the
// table below can describe a running daemon and a stale runtime file separately
// without spawning either.
func useDaemonLiveness(t *testing.T, alive bool) {
	t.Helper()
	previous := syncLockFreeFn
	syncLockFreeFn = func(string) (bool, error) { return !alive, nil }
	t.Cleanup(func() { syncLockFreeFn = previous })
}

const (
	wantDefaultNotice = "                   [!] 실행 중인 데몬은 v0.7.27, 이 바이너리는 v0.7.28 -- 재시작이 필요합니다\n" +
		"                       cctrace sync --stop 뒤 cctrace sync --daemon 으로 다시 띄우면 새 바이너리가 적용됩니다"
	wantProfileNotice = "                   [!] 실행 중인 데몬은 v0.7.27, 이 바이너리는 v0.7.28 -- 재시작이 필요합니다\n" +
		"                       cctrace sync --stop --profile work 뒤 cctrace sync --daemon --profile work 으로 다시 띄우면 새 바이너리가 적용됩니다"
)

// The measured case from #458: disk binary v0.7.28, running watch child v0.7.27,
// server header v0.7.27 -- five days with nothing reporting the split.
//
// The notice is asserted whole. The wording is the product here: it names a
// remedy, and a remedy printed at a user who cannot follow it is worse than
// silence, which is why the dead-daemon row exists.
func TestDaemonRestartNotice(t *testing.T) {
	cases := []struct {
		name           string
		binaryVersion  string
		runtimeVersion string
		writeRuntime   bool
		daemonAlive    bool
		syncEnabled    bool
		profileName    string
		want           string
	}{
		{
			name:           "mismatch with a running daemon",
			binaryVersion:  "v0.7.28",
			runtimeVersion: "v0.7.27",
			writeRuntime:   true,
			daemonAlive:    true,
			syncEnabled:    true,
			want:           wantDefaultNotice,
		},
		{
			name:           "mismatch on a named profile",
			binaryVersion:  "v0.7.28",
			runtimeVersion: "v0.7.27",
			writeRuntime:   true,
			daemonAlive:    true,
			syncEnabled:    true,
			profileName:    "work",
			want:           wantProfileNotice,
		},
		{
			name:           "stale runtime file, no daemon running",
			binaryVersion:  "v0.7.28",
			runtimeVersion: "v0.7.27",
			writeRuntime:   true,
			daemonAlive:    false,
			syncEnabled:    true,
		},
		{
			name:           "sync collection disabled",
			binaryVersion:  "v0.7.28",
			runtimeVersion: "v0.7.27",
			writeRuntime:   true,
			daemonAlive:    true,
			syncEnabled:    false,
		},
		{
			name:           "match",
			binaryVersion:  "v0.7.28",
			runtimeVersion: "v0.7.28",
			writeRuntime:   true,
			daemonAlive:    true,
			syncEnabled:    true,
		},
		{
			name:           "runtime stamp missing",
			binaryVersion:  "v0.7.28",
			runtimeVersion: "",
			writeRuntime:   true,
			daemonAlive:    true,
			syncEnabled:    true,
		},
		{
			name:           "dev build",
			binaryVersion:  "dev",
			runtimeVersion: "v0.7.27",
			writeRuntime:   true,
			daemonAlive:    true,
			syncEnabled:    true,
		},
		{
			name:          "no runtime file",
			binaryVersion: "v0.7.28",
			writeRuntime:  false,
			daemonAlive:   true,
			syncEnabled:   true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			useTempSyncHome(t)
			useDaemonLiveness(t, tc.daemonAlive)
			old := version
			version = tc.binaryVersion
			t.Cleanup(func() { version = old })

			if tc.writeRuntime {
				rt := syncRuntime{
					PID:        1234,
					InstanceID: "x",
					StartedAt:  time.Now().UTC(),
					Version:    tc.runtimeVersion,
				}
				if err := writeJSONFile(runtimeFilePath(tc.profileName), rt); err != nil {
					t.Fatalf("write runtime: %v", err)
				}
			}

			if got := daemonRestartNotice(tc.profileName, tc.syncEnabled); got != tc.want {
				t.Errorf("daemonRestartNotice() =\n%q\nwant\n%q", got, tc.want)
			}
		})
	}
}
