//go:build darwin

package desktop

import (
	"bufio"
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"golang.org/x/sys/unix"
)

const daemonLockChildEnv = "CCTRACE_DAEMON_LOCK_TEST_FILE"
const daemonLockModeEnv = "CCTRACE_DAEMON_LOCK_TEST_MODE"

func TestDaemonLockProbeDarwinReadOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sync.lock")
	if err := os.WriteFile(path, []byte("synthetic-lock-fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	beforeBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	beforeInfo, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	beforeNames, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if state := probeDaemonLock(path); state != daemonLockFree {
		t.Fatalf("unlocked synthetic lock state=%v want free", state)
	}

	command := exec.Command(os.Args[0], "-test.run=^TestDaemonLockProbeDarwinChild$")
	command.Env = []string{daemonLockChildEnv + "=" + path}
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	ready, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || ready != "LOCKED\n" {
		_ = stdin.Close()
		_ = command.Wait()
		t.Fatalf("synthetic lock child did not become ready: line=%q err=%v stderr=%s", ready, err, stderr.String())
	}
	if state := probeDaemonLock(path); state != daemonLockHeld {
		_, _ = io.WriteString(stdin, "release\n")
		_ = stdin.Close()
		_ = command.Wait()
		t.Fatalf("child-held synthetic lock state=%v want held", state)
	}
	if _, err := io.WriteString(stdin, "release\n"); err != nil {
		t.Fatal(err)
	}
	_ = stdin.Close()
	if err := command.Wait(); err != nil {
		t.Fatalf("synthetic lock child: %v; stderr=%s", err, stderr.String())
	}
	if state := probeDaemonLock(path); state != daemonLockFree {
		t.Fatalf("released synthetic lock state=%v want free", state)
	}

	shared := exec.Command(os.Args[0], "-test.run=^TestDaemonLockProbeDarwinChild$")
	shared.Env = []string{daemonLockChildEnv + "=" + path, daemonLockModeEnv + "=shared"}
	sharedIn, err := shared.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	sharedOut, err := shared.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var sharedErr bytes.Buffer
	shared.Stderr = &sharedErr
	if err := shared.Start(); err != nil {
		t.Fatal(err)
	}
	sharedReady, err := bufio.NewReader(sharedOut).ReadString('\n')
	if err != nil || sharedReady != "LOCKED\n" {
		_ = sharedIn.Close()
		_ = shared.Wait()
		t.Fatalf("shared lock child did not become ready: line=%q err=%v stderr=%s", sharedReady, err, sharedErr.String())
	}
	if state := probeDaemonLock(path); state != daemonLockUnknown {
		_, _ = io.WriteString(sharedIn, "release\n")
		_ = sharedIn.Close()
		_ = shared.Wait()
		t.Fatalf("child-held shared lock state=%v want unknown", state)
	}
	_, _ = io.WriteString(sharedIn, "release\n")
	_ = sharedIn.Close()
	if err := shared.Wait(); err != nil {
		t.Fatalf("shared lock child: %v; stderr=%s", err, sharedErr.String())
	}
	afterBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	afterInfo, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	afterNames, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(beforeBytes, afterBytes) || beforeInfo.Size() != afterInfo.Size() ||
		!beforeInfo.ModTime().Equal(afterInfo.ModTime()) || !reflect.DeepEqual(beforeNames, afterNames) {
		t.Fatal("read-only lock probe changed synthetic lock bytes or directory inventory")
	}
}

func TestDaemonLockProbeDarwinRejectsInodeRace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sync.lock")
	if err := os.WriteFile(path, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	state := probeDaemonLockWithHook(path, func() {
		if err := os.Rename(path, path+".old"); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("after!"), 0o600); err != nil {
			t.Fatal(err)
		}
	})
	if state != daemonLockUnknown {
		t.Fatalf("replaced lock inode state=%v want unknown", state)
	}
}

func TestDaemonLockProbeDarwinChild(t *testing.T) {
	path := os.Getenv(daemonLockChildEnv)
	if path == "" {
		return
	}
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	mode := os.Getenv(daemonLockModeEnv)
	lockMode := int(unix.LOCK_EX)
	if mode == "shared" {
		lockMode = unix.LOCK_SH
	}
	if err := unix.Flock(fd, lockMode); err != nil {
		t.Fatal(err)
	}
	defer unix.Flock(fd, unix.LOCK_UN)
	if _, err := io.WriteString(os.Stdout, "LOCKED\n"); err != nil {
		t.Fatal(err)
	}
	_, err = bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
}
