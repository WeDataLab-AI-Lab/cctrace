//go:build linux

package processio

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

func checkOpenFileMappingSupport() error {
	if info, err := os.Stat("/proc/self/fd"); err != nil || !info.IsDir() {
		return errors.New("Linux /proc file-descriptor inspection is unavailable; safe Codex session mapping cannot run")
	}
	return nil
}

func processStartToken(pid int) (string, error) {
	ticks, err := linuxProcessTicks(pid)
	if err != nil {
		return "", err
	}
	return "linux:" + strconv.FormatUint(ticks, 10), nil
}

func linuxProcessTicks(pid int) (uint64, error) {
	if pid <= 0 {
		return 0, errors.New("invalid process id")
	}
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, err
	}
	close := bytes.LastIndexByte(data, ')')
	if close < 0 {
		return 0, errors.New("process stat has no command terminator")
	}
	fields := strings.Fields(string(data[close+1:]))
	if len(fields) < 20 {
		return 0, errors.New("process stat has no start token")
	}
	return strconv.ParseUint(fields[19], 10, 64)
}

func matchesClaudeProcStart(pid int, procStart, procStartFt string) bool {
	if procStartFt != "" || procStart == "" {
		return false
	}
	actual, err := claudeProcStartForPID(pid)
	if err != nil {
		return false
	}
	want, err := time.ParseInLocation("Mon Jan _2 15:04:05 2006", strings.TrimSpace(procStart), time.UTC)
	if err != nil {
		return false
	}
	got, err := time.ParseInLocation("Mon Jan _2 15:04:05 2006", actual, time.UTC)
	if err != nil {
		return false
	}
	delta := got.Sub(want)
	return delta >= -2*time.Second && delta <= 2*time.Second
}

func claudeProcStartForPID(pid int) (string, error) {
	ticks, err := linuxProcessTicks(pid)
	if err != nil {
		return "", err
	}
	boot, err := linuxBootTime()
	if err != nil {
		return "", err
	}
	hz := uint64(100)
	if auxv, err := unix.Auxv(); err == nil {
		for _, entry := range auxv {
			if entry[0] == 17 && entry[1] > 0 {
				hz = uint64(entry[1])
				break
			}
		}
	}
	start := boot.Add(time.Duration(ticks/hz)*time.Second + time.Duration(ticks%hz)*time.Second/time.Duration(hz))
	return start.UTC().Format("Mon Jan _2 15:04:05 2006"), nil
}

func linuxBootTime() (time.Time, error) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return time.Time{}, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if value, ok := strings.CutPrefix(line, "btime "); ok {
			seconds, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
			if err != nil {
				return time.Time{}, err
			}
			return time.Unix(seconds, 0), nil
		}
	}
	return time.Time{}, errors.New("Linux boot time is unavailable")
}

func processOpenFiles(pid int) ([]string, error) {
	if pid <= 0 {
		return nil, errors.New("invalid process id")
	}
	root := filepath.Join("/proc", strconv.Itoa(pid), "fd")
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		path, err := os.Readlink(filepath.Join(root, entry.Name()))
		if err == nil {
			paths = append(paths, path)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
	}
	return paths, nil
}
