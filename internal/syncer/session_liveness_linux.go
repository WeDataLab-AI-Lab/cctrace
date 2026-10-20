package syncer

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// processStart reports on the process with pid: whether there is one, and when
// it started. Field 22 of /proc/<pid>/stat, starttime, counts clock ticks from
// boot and never changes while the process lives; it is the process's identity.
// The wall-clock start is the boot time plus that, and the boot time Linux gives
// (/proc/stat btime) is the wall clock now minus the time since boot -- so every
// step of the wall clock since the process started moves it.
func processStart(pid int) (processStartInfo, error) {
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if errors.Is(err, fs.ErrNotExist) {
		return processStartInfo{}, nil
	}
	if err != nil {
		return processStartInfo{}, err
	}
	// Field 2, the command name, is in parentheses and may hold spaces and
	// parentheses of its own, so fields are counted from the last ')': field 3
	// is the first after it, which puts field 22 at index 19.
	i := bytes.LastIndexByte(stat, ')')
	if i < 0 {
		return processStartInfo{}, fmt.Errorf("/proc/%d/stat: no command name", pid)
	}
	fields := strings.Fields(string(stat[i+1:]))
	if len(fields) < 20 {
		return processStartInfo{}, fmt.Errorf("/proc/%d/stat: %d fields after the command name", pid, len(fields))
	}
	ticks, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return processStartInfo{}, fmt.Errorf("/proc/%d/stat starttime: %w", pid, err)
	}
	boot, err := bootTimeFn()
	if err != nil {
		return processStartInfo{}, err
	}
	hz := clockTicks()
	// Whole seconds first: ticks times time.Second overflows after some three
	// years of uptime.
	at := boot.Add(time.Duration(ticks/hz)*time.Second + time.Duration(ticks%hz)*time.Second/time.Duration(hz))
	return processStartInfo{exists: true, at: at, id: int64(ticks)}, nil
}

// runsAnotherProgram reports whether the process with pid plainly runs another
// program than Claude Code: neither its executable nor its arguments mention
// claude -- not the native build under .../claude/versions/, not the npm package
// @anthropic-ai/claude-code, not a `claude` command. What cannot be read, such
// as another user's process, is not known to be another program.
func runsAnotherProgram(pid int) bool {
	exe, args, err := programOf(pid)
	if err != nil {
		return false
	}
	for _, s := range append(args, exe) {
		if strings.Contains(strings.ToLower(s), "claude") {
			return false
		}
	}
	return true
}

// programOf is what the process with pid runs: the executable /proc links to,
// "" for a kernel thread or a zombie, which have none, and its arguments. A var
// so a test can make it unreadable.
var programOf = func(pid int) (string, []string, error) {
	dir := "/proc/" + strconv.Itoa(pid)
	exe, err := os.Readlink(dir + "/exe")
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", nil, err
	}
	cmdline, err := os.ReadFile(dir + "/cmdline")
	if err != nil {
		return "", nil, err
	}
	return exe, strings.Split(strings.TrimRight(string(cmdline), "\x00"), "\x00"), nil
}

// bootTimeFn is bootTime. A var so a test can step the wall clock.
var bootTimeFn = bootTime

// bootTime is when the system booted by the wall clock now, from the btime line
// of /proc/stat.
func bootTime() (time.Time, error) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return time.Time{}, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if v, ok := strings.CutPrefix(line, "btime "); ok {
			secs, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
			if err != nil {
				return time.Time{}, fmt.Errorf("/proc/stat btime: %w", err)
			}
			return time.Unix(secs, 0), nil
		}
	}
	return time.Time{}, errors.New("/proc/stat has no btime line")
}

// atClockTick is AT_CLKTCK, the auxiliary vector entry for the clock tick rate.
const atClockTick = 17

// clockTicks is the rate /proc/<pid>/stat counts in, what sysconf(_SC_CLK_TCK)
// returns: AT_CLKTCK from the auxiliary vector, else 100, the USER_HZ of every
// architecture Go builds for on Linux.
func clockTicks() uint64 {
	if auxv, err := unix.Auxv(); err == nil {
		for _, kv := range auxv {
			if kv[0] == atClockTick && kv[1] > 0 {
				return uint64(kv[1])
			}
		}
	}
	return 100
}

// localPIDDomain is the pidDomain Claude Code writes on Linux and WSL: the
// machine and the pid namespace, in its form "linux:<machine id>:<namespace>".
// Either part is empty when it cannot be read, as Claude Code leaves it. A pid
// means the same process only within one domain: another machine's file, or one
// from a container with its own pid namespace, is not this client's to check.
var localPIDDomain = sync.OnceValue(func() string {
	machineID, _ := os.ReadFile("/etc/machine-id")
	namespace, _ := os.Readlink("/proc/self/ns/pid")
	return "linux:" + strings.TrimSpace(string(machineID)) + ":" + namespace
})
