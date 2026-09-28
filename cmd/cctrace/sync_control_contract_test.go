package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cctrace/internal/profile"
)

func TestRunSyncStartContract(t *testing.T) {
	useTempSyncHome(t)
	resetSyncControlHooks(t)
	p := profile.NewDefault()
	p.Server.Endpoint = "http://otel.example.com:4317"
	if err := profile.SaveNamed(p, "work"); err != nil {
		t.Fatalf("profile.SaveNamed: %v", err)
	}
	var gotArgs []string
	spawnSyncProcessFn = func(args []string, profileName string) (int, error) {
		gotArgs = append([]string(nil), args...)
		if profileName != "work" {
			t.Fatalf("profileName = %q, want work", profileName)
		}
		return 321, nil
	}
	waitForSyncStartAckFn = func(path string, timeout time.Duration) (*syncStartAck, error) {
		if path == "" || filepath.Dir(path) != syncProfileDir("work") {
			t.Fatalf("ack path = %q, want inside named profile dir", path)
		}
		if timeout != syncStartAckWait {
			t.Fatalf("timeout = %s, want %s", timeout, syncStartAckWait)
		}
		return &syncStartAck{Status: syncStartAckAlreadyRunning}, nil
	}

	if err := runSyncStart("/tmp/claude dir", 2*time.Second, "work", "user@example.com", "http://sync.example.com", true); err != nil {
		t.Fatalf("runSyncStart: %v", err)
	}
	want := []string{
		"sync", "--watch", "--interval", "2s", "--start-ack-file", gotArgs[5],
		"--claude-dir", "/tmp/claude dir",
		"--profile", "work",
		"--profile-email", "user@example.com",
		"--endpoint", "http://sync.example.com",
		"--local",
	}
	assertArgsEqual(t, gotArgs, want)
}

func TestRunSyncStartErrorContracts(t *testing.T) {
	t.Run("missing profile", func(t *testing.T) {
		useTempSyncHome(t)
		resetSyncControlHooks(t)
		err := runSyncStart("", time.Second, "", "", "", false)
		if err == nil || !strings.Contains(err.Error(), "no profile found") {
			t.Fatalf("error = %v, want missing profile", err)
		}
	})

	t.Run("disabled", func(t *testing.T) {
		useTempSyncHome(t)
		resetSyncControlHooks(t)
		p := profile.NewDefault()
		p.Options.SyncEnabled = false
		p.Server.Endpoint = "http://otel.example.com:4317"
		if err := profile.Save(p); err != nil {
			t.Fatalf("profile.Save: %v", err)
		}
		spawnSyncProcessFn = func(args []string, profileName string) (int, error) {
			t.Fatal("disabled sync must not spawn")
			return 0, nil
		}
		if err := runSyncStart("", time.Second, "", "", "", false); err != nil {
			t.Fatalf("runSyncStart disabled: %v", err)
		}
	})

	t.Run("missing endpoint", func(t *testing.T) {
		useTempSyncHome(t)
		resetSyncControlHooks(t)
		if err := profile.Save(profile.NewDefault()); err != nil {
			t.Fatalf("profile.Save: %v", err)
		}
		err := runSyncStart("", time.Second, "", "", "", false)
		if err == nil || !strings.Contains(err.Error(), "no server endpoint") {
			t.Fatalf("error = %v, want missing endpoint", err)
		}
	})

	t.Run("spawn", func(t *testing.T) {
		useTempSyncHome(t)
		resetSyncControlHooks(t)
		p := profile.NewDefault()
		p.Server.Endpoint = "http://otel.example.com:4317"
		if err := profile.Save(p); err != nil {
			t.Fatalf("profile.Save: %v", err)
		}
		spawnSyncProcessFn = func(args []string, profileName string) (int, error) {
			return 0, errors.New("spawn failed")
		}
		err := runSyncStart("", time.Second, "", "", "", false)
		if err == nil || !strings.Contains(err.Error(), "spawn failed") {
			t.Fatalf("error = %v, want spawn failed", err)
		}
	})

	for _, tc := range []struct {
		name    string
		ack     *syncStartAck
		waitErr error
		wantErr string
	}{
		{name: "wait error", waitErr: errors.New("ack failed"), wantErr: "pid 777"},
		{name: "empty error status", ack: &syncStartAck{Status: syncStartAckError}, wantErr: "sync start failed"},
		{name: "error status message", ack: &syncStartAck{Status: syncStartAckError, Error: "boom"}, wantErr: "boom"},
		{name: "unknown status", ack: &syncStartAck{Status: "mystery"}, wantErr: `unknown sync start acknowledgement status "mystery"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			useTempSyncHome(t)
			resetSyncControlHooks(t)
			p := profile.NewDefault()
			p.Server.Endpoint = "http://otel.example.com:4317"
			if err := profile.Save(p); err != nil {
				t.Fatalf("profile.Save: %v", err)
			}
			spawnSyncProcessFn = func(args []string, profileName string) (int, error) {
				return 777, nil
			}
			waitForSyncStartAckFn = func(path string, timeout time.Duration) (*syncStartAck, error) {
				return tc.ack, tc.waitErr
			}
			err := runSyncStart("", time.Second, "", "", "", false)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestRunSyncStartStartedAck(t *testing.T) {
	useTempSyncHome(t)
	resetSyncControlHooks(t)
	p := profile.NewDefault()
	p.Server.Endpoint = "http://otel.example.com:4317"
	if err := profile.Save(p); err != nil {
		t.Fatalf("profile.Save: %v", err)
	}
	spawnSyncProcessFn = func(args []string, profileName string) (int, error) {
		return 123, nil
	}
	waitForSyncStartAckFn = func(path string, timeout time.Duration) (*syncStartAck, error) {
		return &syncStartAck{Status: syncStartAckStarted, InstanceID: "inst"}, nil
	}
	if err := runSyncStart("", time.Second, "", "", "", false); err != nil {
		t.Fatalf("runSyncStart: %v", err)
	}
}

func TestRunSyncStartLocalAllowsBlankStoredEndpoint(t *testing.T) {
	useTempSyncHome(t)
	resetSyncControlHooks(t)
	p := profile.NewDefault()
	p.Server.Endpoint = ""
	if err := profile.Save(p); err != nil {
		t.Fatalf("profile.Save: %v", err)
	}
	var gotArgs []string
	spawnSyncProcessFn = func(args []string, profileName string) (int, error) {
		gotArgs = append([]string(nil), args...)
		return 321, nil
	}
	waitForSyncStartAckFn = func(path string, timeout time.Duration) (*syncStartAck, error) {
		return &syncStartAck{Status: syncStartAckStarted}, nil
	}

	if err := runSyncStart("", time.Second, "", "", "", true); err != nil {
		t.Fatalf("runSyncStart local with blank endpoint: %v", err)
	}
	if !hasArg(gotArgs, "--local") {
		t.Fatalf("local start args missing --local: %v", gotArgs)
	}
	if !hasArg(gotArgs, "--endpoint") {
		t.Fatalf("local start args missing resolved --endpoint: %v", gotArgs)
	}
}

func TestRunSyncFinalizeContract(t *testing.T) {
	useTempSyncHome(t)
	resetSyncControlHooks(t)
	rt := &syncRuntime{PID: 1234, InstanceID: "inst-final", StartedAt: time.Now().UTC()}
	if err := writeJSONFile(runtimeFilePath("work"), rt); err != nil {
		t.Fatalf("write runtime: %v", err)
	}
	var waitedInstance string
	waitForWatcherExitFn = func(profileName string, instanceID string, timeout time.Duration) (bool, error) {
		if profileName != "work" {
			t.Fatalf("profileName = %q, want work", profileName)
		}
		waitedInstance = instanceID
		return true, nil
	}
	var gotArgs []string
	spawnSyncProcessFn = func(args []string, profileName string) (int, error) {
		gotArgs = append([]string(nil), args...)
		return 55, nil
	}

	if err := runSyncFinalize("/tmp/claude", "work", "user@example.com", "http://sync.example.com", true); err != nil {
		t.Fatalf("runSyncFinalize: %v", err)
	}
	if waitedInstance != rt.InstanceID {
		t.Fatalf("waited instance = %q, want %q", waitedInstance, rt.InstanceID)
	}
	req, ok, err := readSyncStopRequest("work")
	if err != nil || !ok || req.InstanceID != rt.InstanceID {
		t.Fatalf("stop request = %+v ok=%v err=%v", req, ok, err)
	}
	assertArgsEqual(t, gotArgs, []string{
		"sync", "--finalize-worker",
		"--claude-dir", "/tmp/claude",
		"--profile", "work",
		"--profile-email", "user@example.com",
		"--endpoint", "http://sync.example.com",
		"--local",
	})
}

func TestRunSyncFinalizeStillRunning(t *testing.T) {
	useTempSyncHome(t)
	resetSyncControlHooks(t)
	waitForWatcherExitFn = func(profileName string, instanceID string, timeout time.Duration) (bool, error) {
		return false, nil
	}
	spawnSyncProcessFn = func(args []string, profileName string) (int, error) {
		return 55, nil
	}
	if err := runSyncFinalize("", "", "", "", false); err != nil {
		t.Fatalf("runSyncFinalize: %v", err)
	}
}

func TestRunSyncFinalizeErrorContracts(t *testing.T) {
	t.Run("read runtime", func(t *testing.T) {
		useTempSyncHome(t)
		if err := os.MkdirAll(runtimeFilePath(""), 0700); err != nil {
			t.Fatalf("mkdir runtime: %v", err)
		}
		err := runSyncFinalize("", "", "", "", false)
		if err == nil || !strings.Contains(err.Error(), "read watcher runtime") {
			t.Fatalf("error = %v, want read runtime", err)
		}
	})

	t.Run("write stop", func(t *testing.T) {
		useTempSyncHome(t)
		rt := &syncRuntime{PID: 1, InstanceID: "inst", StartedAt: time.Now().UTC()}
		if err := writeJSONFile(runtimeFilePath(""), rt); err != nil {
			t.Fatalf("write runtime: %v", err)
		}
		if err := os.MkdirAll(stopRequestFilePath(""), 0700); err != nil {
			t.Fatalf("mkdir stop path: %v", err)
		}
		err := runSyncFinalize("", "", "", "", false)
		if err == nil || !strings.Contains(err.Error(), "write stop request") {
			t.Fatalf("error = %v, want write stop request", err)
		}
	})

	t.Run("wait", func(t *testing.T) {
		useTempSyncHome(t)
		resetSyncControlHooks(t)
		waitForWatcherExitFn = func(profileName string, instanceID string, timeout time.Duration) (bool, error) {
			return false, errors.New("wait failed")
		}
		err := runSyncFinalize("", "", "", "", false)
		if err == nil || !strings.Contains(err.Error(), "wait failed") {
			t.Fatalf("error = %v, want wait failed", err)
		}
	})

	t.Run("spawn worker", func(t *testing.T) {
		useTempSyncHome(t)
		resetSyncControlHooks(t)
		waitForWatcherExitFn = func(profileName string, instanceID string, timeout time.Duration) (bool, error) {
			return true, nil
		}
		spawnSyncProcessFn = func(args []string, profileName string) (int, error) {
			return 0, errors.New("worker spawn failed")
		}
		err := runSyncFinalize("", "", "", "", false)
		if err == nil || !strings.Contains(err.Error(), "worker spawn failed") {
			t.Fatalf("error = %v, want worker spawn failed", err)
		}
	})
}

func TestRunSyncFinalizeWorkerContract(t *testing.T) {
	t.Run("lock held exits", func(t *testing.T) {
		useTempSyncHome(t)
		resetSyncControlHooks(t)
		fl, err := acquireFileLock(finalizeLockFilePath(""), 0)
		if err != nil {
			t.Fatalf("acquire finalize lock: %v", err)
		}
		defer fl.Unlock()
		runSyncFn = func(dryRun bool, claudeDir string, watch bool, daemon bool, daemonOnce bool, stop bool, interval time.Duration, profileName string, profileEmail string, endpointOverride string, local bool, startAckFile string, finalizeWorker bool) error {
			t.Fatal("second finalize worker must not run sync")
			return nil
		}
		if err := runSyncFinalizeWorker("", "", "", "", false); err != nil {
			t.Fatalf("runSyncFinalizeWorker: %v", err)
		}
	})

	t.Run("runs one shot", func(t *testing.T) {
		useTempSyncHome(t)
		resetSyncControlHooks(t)
		called := false
		runSyncFn = func(dryRun bool, claudeDir string, watch bool, daemon bool, daemonOnce bool, stop bool, interval time.Duration, profileName string, profileEmail string, endpointOverride string, local bool, startAckFile string, finalizeWorker bool) error {
			called = true
			if dryRun || watch || daemon || daemonOnce || stop || !finalizeWorker || interval != 30*time.Second {
				t.Fatalf("unexpected finalize sync mode")
			}
			return nil
		}
		if err := runSyncFinalizeWorker("", "", "", "", false); err != nil {
			t.Fatalf("runSyncFinalizeWorker: %v", err)
		}
		if !called {
			t.Fatal("finalize worker did not run sync")
		}
	})

	t.Run("lock error", func(t *testing.T) {
		home := filepath.Join(t.TempDir(), "home-file")
		if err := os.WriteFile(home, []byte("x"), 0600); err != nil {
			t.Fatalf("write home file: %v", err)
		}
		t.Setenv("HOME", home)
		t.Setenv("USERPROFILE", home)
		err := runSyncFinalizeWorker("", "", "", "", false)
		if err == nil || !strings.Contains(err.Error(), "acquire finalize lock") {
			t.Fatalf("error = %v, want finalize lock", err)
		}
	})
}

func TestSyncControlFileContracts(t *testing.T) {
	t.Run("load invalid profile", func(t *testing.T) {
		useTempSyncHome(t)
		if err := os.MkdirAll(profile.DefaultDir(), 0700); err != nil {
			t.Fatalf("mkdir profile dir: %v", err)
		}
		if err := os.WriteFile(profile.DefaultPath(), []byte("{bad"), 0600); err != nil {
			t.Fatalf("write profile: %v", err)
		}
		if _, err := loadSyncProfile(""); err == nil || !strings.Contains(err.Error(), "load profile") {
			t.Fatalf("error = %v, want load profile", err)
		}
	})

	t.Run("ack empty path", func(t *testing.T) {
		if err := writeSyncStartAck("", syncStartAck{Status: syncStartAckStarted}); err != nil {
			t.Fatalf("write empty ack: %v", err)
		}
	})

	t.Run("ack timeout", func(t *testing.T) {
		_, err := waitForSyncStartAck(filepath.Join(t.TempDir(), "missing.json"), 20*time.Millisecond)
		if err == nil || !strings.Contains(err.Error(), "timed out") {
			t.Fatalf("error = %v, want timeout", err)
		}
	})

	t.Run("ack read error waits until deadline", func(t *testing.T) {
		ackPath := filepath.Join(t.TempDir(), "ack.json")
		if err := os.MkdirAll(ackPath, 0700); err != nil {
			t.Fatalf("mkdir ack path: %v", err)
		}
		_, err := waitForSyncStartAck(ackPath, 20*time.Millisecond)
		if err == nil {
			t.Fatal("expected ack read error after deadline")
		}
	})

	t.Run("read json error", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "as-dir")
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatalf("mkdir path: %v", err)
		}
		var rt syncRuntime
		if ok, err := readJSONFile(path, &rt); err == nil || ok {
			t.Fatalf("ok=%v err=%v, want read error", ok, err)
		}
	})

	t.Run("read json no data cases", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "runtime.json")
		var rt syncRuntime
		if ok, err := readJSONFile(path, &rt); err != nil || ok {
			t.Fatalf("missing ok=%v err=%v", ok, err)
		}
		if err := os.WriteFile(path, nil, 0600); err != nil {
			t.Fatalf("write empty: %v", err)
		}
		if ok, err := readJSONFile(path, &rt); err != nil || ok {
			t.Fatalf("empty ok=%v err=%v", ok, err)
		}
		if err := os.WriteFile(path, []byte("{bad"), 0600); err != nil {
			t.Fatalf("write invalid: %v", err)
		}
		if ok, err := readJSONFile(path, &rt); err != nil || ok {
			t.Fatalf("invalid ok=%v err=%v", ok, err)
		}
	})

	t.Run("read sync wrappers propagate errors", func(t *testing.T) {
		useTempSyncHome(t)
		if err := os.MkdirAll(stopRequestFilePath(""), 0700); err != nil {
			t.Fatalf("mkdir stop path: %v", err)
		}
		if _, ok, err := readSyncStopRequest(""); err == nil || ok {
			t.Fatalf("stop ok=%v err=%v, want error", ok, err)
		}
		ackPath := filepath.Join(t.TempDir(), "ack")
		if err := os.MkdirAll(ackPath, 0700); err != nil {
			t.Fatalf("mkdir ack path: %v", err)
		}
		if _, ok, err := readSyncStartAck(ackPath); err == nil || ok {
			t.Fatalf("ack ok=%v err=%v, want error", ok, err)
		}
	})

	t.Run("write json errors", func(t *testing.T) {
		if err := writeJSONFile(filepath.Join(t.TempDir(), "bad.json"), func() {}); err == nil {
			t.Fatal("expected marshal error")
		}
		parent := filepath.Join(t.TempDir(), "not-dir")
		if err := os.WriteFile(parent, []byte("x"), 0600); err != nil {
			t.Fatalf("write parent: %v", err)
		}
		if err := writeJSONFile(filepath.Join(parent, "bad.json"), map[string]string{"ok": "true"}); err == nil {
			t.Fatal("expected parent error")
		}
		dirPath := filepath.Join(t.TempDir(), "value.json")
		if err := os.MkdirAll(dirPath, 0700); err != nil {
			t.Fatalf("mkdir dest dir: %v", err)
		}
		if err := writeJSONFile(dirPath, map[string]string{"ok": "true"}); err == nil {
			t.Fatal("expected rename over directory error")
		}
	})
}

func TestSyncControlPIDAndLockContracts(t *testing.T) {
	t.Run("current pid variants", func(t *testing.T) {
		useTempSyncHome(t)
		if pid, ok, err := currentWatcherPID("", &syncRuntime{PID: 111}); err != nil || !ok || pid != 111 {
			t.Fatalf("runtime pid=%d ok=%v err=%v", pid, ok, err)
		}
		if pid, ok, err := currentWatcherPID("", nil); err != nil || ok || pid != 0 {
			t.Fatalf("missing pid=%d ok=%v err=%v", pid, ok, err)
		}
		if err := os.MkdirAll(syncProfileDir(""), 0700); err != nil {
			t.Fatalf("mkdir sync dir: %v", err)
		}
		if err := writePID(pidFilePath(""), 222); err != nil {
			t.Fatalf("write pid: %v", err)
		}
		if pid, ok, err := currentWatcherPID("", nil); err != nil || !ok || pid != 222 {
			t.Fatalf("pid file pid=%d ok=%v err=%v", pid, ok, err)
		}
		if err := os.WriteFile(pidFilePath(""), []byte("bad"), 0600); err != nil {
			t.Fatalf("write invalid pid: %v", err)
		}
		if pid, ok, err := currentWatcherPID("", nil); err != nil || ok || pid != 0 {
			t.Fatalf("invalid pid=%d ok=%v err=%v", pid, ok, err)
		}
	})

	t.Run("lock helpers", func(t *testing.T) {
		useTempSyncHome(t)
		if err := waitForSyncUnlock("", 20*time.Millisecond); err != nil {
			t.Fatalf("waitForSyncUnlock: %v", err)
		}
		parent := filepath.Join(t.TempDir(), "not-dir")
		if err := os.WriteFile(parent, []byte("x"), 0600); err != nil {
			t.Fatalf("write parent: %v", err)
		}
		if _, err := acquireFileLock(filepath.Join(parent, "sync.lock"), 0); err == nil {
			t.Fatal("expected acquire parent error")
		}
		if err := waitForFileUnlock(filepath.Join(parent, "sync.lock"), 20*time.Millisecond); err == nil {
			t.Fatal("expected wait parent error")
		}
	})

	t.Run("start runtime failure cleanup", func(t *testing.T) {
		useTempSyncHome(t)
		if err := os.MkdirAll(runtimeFilePath(""), 0700); err != nil {
			t.Fatalf("mkdir runtime path: %v", err)
		}
		err := startWatchRuntime("", &syncRuntime{PID: 333, InstanceID: "inst", StartedAt: time.Now().UTC()})
		if err == nil {
			t.Fatal("expected runtime write error")
		}
		if _, err := os.Stat(pidFilePath("")); !os.IsNotExist(err) {
			t.Fatalf("pid file should be removed, stat err=%v", err)
		}
	})
}

func TestSpawnSyncProcessContract(t *testing.T) {
	useTempSyncHome(t)
	resetSyncControlHooks(t)
	syncExecutablePathFn = func() (string, error) {
		return "/usr/local/bin/cctrace", nil
	}
	startProcessFn = func(exe string, argv []string, attr *os.ProcAttr) (*os.Process, error) {
		if exe != "/usr/local/bin/cctrace" {
			t.Fatalf("exe=%q", exe)
		}
		// --log-to-file is added here, not by the caller: this is the one place
		// that points a child's stdout and stderr at the crash file, so it is the
		// one place that must tell the child to log elsewhere. Any spawn path
		// that lost the flag would quietly fill the crash log with routine
		// output and hasten the truncation that discards crash evidence.
		assertArgsEqual(t, argv, []string{"/usr/local/bin/cctrace", "sync", "--watch", "--log-to-file"})
		if attr.Dir != "/" || len(attr.Files) != 3 || attr.Files[1] != attr.Files[2] {
			t.Fatalf("bad proc attr: %#v", attr)
		}
		return &os.Process{Pid: 4321}, nil
	}
	released := false
	releaseProcessFn = func(proc *os.Process) error {
		released = true
		return nil
	}
	pid, err := spawnSyncProcess([]string{"sync", "--watch"}, "work")
	if err != nil {
		t.Fatalf("spawnSyncProcess: %v", err)
	}
	if pid != 4321 || !released {
		t.Fatalf("pid=%d released=%v", pid, released)
	}
	if _, err := os.Stat(syncLogPath("work")); err != nil {
		t.Fatalf("sync log missing: %v", err)
	}
}

// Every spawn path must carry the flag, including the finalize worker, whose
// routine output otherwise lands in the crash log with no logger installed.
func TestSpawnSyncProcessAlwaysRoutesChildLogging(t *testing.T) {
	for _, args := range [][]string{
		{"sync", "--watch", "--interval", "1s"},
		{"sync", "--once"},
		buildFinalizeWorkerArgs("", "work", "", "", false),
	} {
		useTempSyncHome(t)
		resetSyncControlHooks(t)
		syncExecutablePathFn = func() (string, error) { return "/usr/local/bin/cctrace", nil }
		var got []string
		startProcessFn = func(exe string, argv []string, attr *os.ProcAttr) (*os.Process, error) {
			got = argv
			return &os.Process{Pid: 1}, nil
		}
		releaseProcessFn = func(proc *os.Process) error { return nil }

		if _, err := spawnSyncProcess(args, "work"); err != nil {
			t.Fatalf("spawnSyncProcess(%v): %v", args, err)
		}
		if len(got) == 0 || got[len(got)-1] != "--log-to-file" {
			t.Fatalf("spawn %v produced argv %v, want it to end with --log-to-file", args, got)
		}
	}
}

func TestSpawnSyncProcessErrors(t *testing.T) {
	t.Run("executable", func(t *testing.T) {
		resetSyncControlHooks(t)
		syncExecutablePathFn = func() (string, error) { return "", errors.New("no executable") }
		if _, err := spawnSyncProcess([]string{"sync"}, ""); err == nil || !strings.Contains(err.Error(), "resolve executable") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("start", func(t *testing.T) {
		useTempSyncHome(t)
		resetSyncControlHooks(t)
		syncExecutablePathFn = func() (string, error) { return "/usr/local/bin/cctrace", nil }
		startProcessFn = func(exe string, argv []string, attr *os.ProcAttr) (*os.Process, error) {
			return nil, errors.New("start failed")
		}
		if _, err := spawnSyncProcess([]string{"sync"}, ""); err == nil || !strings.Contains(err.Error(), "start process") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("profile dir", func(t *testing.T) {
		resetSyncControlHooks(t)
		home := filepath.Join(t.TempDir(), "home-file")
		if err := os.WriteFile(home, []byte("x"), 0600); err != nil {
			t.Fatalf("write home file: %v", err)
		}
		t.Setenv("HOME", home)
		t.Setenv("USERPROFILE", home)
		syncExecutablePathFn = func() (string, error) { return "/usr/local/bin/cctrace", nil }
		if _, err := spawnSyncProcess([]string{"sync"}, ""); err == nil || !strings.Contains(err.Error(), "create sync profile dir") {
			t.Fatalf("error = %v, want profile dir", err)
		}
	})

	t.Run("log file", func(t *testing.T) {
		useTempSyncHome(t)
		resetSyncControlHooks(t)
		syncExecutablePathFn = func() (string, error) { return "/usr/local/bin/cctrace", nil }
		if err := os.MkdirAll(syncLogPath(""), 0700); err != nil {
			t.Fatalf("mkdir log path: %v", err)
		}
		if _, err := spawnSyncProcess([]string{"sync"}, ""); err == nil || !strings.Contains(err.Error(), "open log file") {
			t.Fatalf("error = %v, want log file", err)
		}
	})
}

func TestWatcherLockAndContextContracts(t *testing.T) {
	t.Run("watcher stopped lock error", func(t *testing.T) {
		home := filepath.Join(t.TempDir(), "home-file")
		if err := os.WriteFile(home, []byte("x"), 0600); err != nil {
			t.Fatalf("write home file: %v", err)
		}
		t.Setenv("HOME", home)
		t.Setenv("USERPROFILE", home)
		if stopped, err := watcherStopped("", ""); err == nil || stopped {
			t.Fatalf("stopped=%v err=%v, want lock error", stopped, err)
		}
		if stopped, err := waitForWatcherExit("", "", 20*time.Millisecond); err == nil || stopped {
			t.Fatalf("stopped=%v err=%v, want wait lock error", stopped, err)
		}
	})

	t.Run("acquire watch runtime read error", func(t *testing.T) {
		useTempSyncHome(t)
		fl, err := acquireSyncLock("", 0)
		if err != nil {
			t.Fatalf("acquireSyncLock: %v", err)
		}
		defer fl.Unlock()
		if err := os.MkdirAll(runtimeFilePath(""), 0700); err != nil {
			t.Fatalf("mkdir runtime path: %v", err)
		}
		if _, err := acquireWatchSyncLock(""); err == nil {
			t.Fatal("expected runtime read error")
		}
	})

	t.Run("parent context", func(t *testing.T) {
		useTempSyncHome(t)
		parent, cancel := context.WithCancel(context.Background())
		ctx, stopped := newWatchSyncContext(parent, "", "inst-parent")
		cancel()
		select {
		case <-stopped:
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for parent cancel")
		}
		if err := ctx.Err(); err == nil {
			t.Fatal("expected canceled context")
		}
	})
}
