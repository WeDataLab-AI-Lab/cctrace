//go:build darwin

package processio

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
	"unsafe"

	"github.com/ebitengine/purego"
	"golang.org/x/sys/unix"
)

const (
	procPIDListFDs          = 1
	procPIDFDVnodePathInfo  = 2
	procFDTypeVnode         = 1
	procFDInfoSize          = 8
	procVnodePathInfoBuffer = 4096
)

var (
	libprocOnce   sync.Once
	libprocErr    error
	procPIDInfo   func(pid int32, flavor int32, arg uint64, buffer unsafe.Pointer, size int32) int32
	procPIDFDInfo func(pid int32, fd int32, flavor int32, buffer unsafe.Pointer, size int32) int32
)

func checkOpenFileMappingSupport() error {
	if err := loadLibproc(); err != nil {
		return errors.New("macOS libproc file-descriptor inspection is unavailable; safe Codex session mapping cannot run")
	}
	if _, err := processOpenFiles(os.Getpid()); err != nil {
		return errors.New("macOS libproc cannot inspect this process's open files; safe Codex session mapping cannot run")
	}
	return nil
}

func processStartToken(pid int) (string, error) {
	if pid <= 0 {
		return "", errors.New("invalid process id")
	}
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("darwin:%d:%d", info.Proc.P_starttime.Sec, info.Proc.P_starttime.Usec), nil
}

func matchesClaudeProcStart(pid int, procStart, procStartFt string) bool {
	if procStartFt != "" || procStart == "" {
		return false
	}
	want, err := claudeProcStartForPID(pid)
	if err != nil {
		return false
	}
	return want == strings.TrimSpace(procStart)
}

func claudeProcStartForPID(pid int) (string, error) {
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return "", err
	}
	seconds, _ := info.Proc.P_starttime.Unix()
	return time.Unix(seconds, 0).UTC().Format("Mon Jan _2 15:04:05 2006"), nil
}

func processOpenFiles(pid int) ([]string, error) {
	if pid <= 0 {
		return nil, errors.New("invalid process id")
	}
	if err := loadLibproc(); err != nil {
		return nil, err
	}
	size := 8 << 10
	var fds []byte
	for {
		fds = make([]byte, size)
		count := procPIDInfo(int32(pid), procPIDListFDs, 0, unsafe.Pointer(&fds[0]), int32(len(fds)))
		if count < 0 {
			return nil, errors.New("libproc could not list process file descriptors")
		}
		if int(count) < len(fds) {
			fds = fds[:count]
			break
		}
		if size >= 1<<20 {
			return nil, errors.New("libproc file-descriptor list exceeds the inspection limit")
		}
		size *= 2
	}
	if len(fds)%procFDInfoSize != 0 {
		return nil, errors.New("libproc returned a malformed file-descriptor list")
	}
	var paths []string
	for offset := 0; offset < len(fds); offset += procFDInfoSize {
		fd := int32(binary.LittleEndian.Uint32(fds[offset : offset+4]))
		typeID := binary.LittleEndian.Uint32(fds[offset+4 : offset+8])
		if typeID != procFDTypeVnode {
			continue
		}
		buffer := make([]byte, procVnodePathInfoBuffer)
		n := procPIDFDInfo(int32(pid), fd, procPIDFDVnodePathInfo, unsafe.Pointer(&buffer[0]), int32(len(buffer)))
		if n <= 0 || int(n) > len(buffer) {
			continue
		}
		if path := darwinVnodePath(buffer[:n]); path != "" {
			paths = append(paths, path)
		}
	}
	return paths, nil
}

func loadLibproc() error {
	libprocOnce.Do(func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				libprocErr = fmt.Errorf("libproc symbols unavailable: %v", recovered)
			}
		}()
		handle, err := purego.Dlopen("/usr/lib/libproc.dylib", purego.RTLD_LAZY)
		if err != nil {
			libprocErr = err
			return
		}
		purego.RegisterLibFunc(&procPIDInfo, handle, "proc_pidinfo")
		purego.RegisterLibFunc(&procPIDFDInfo, handle, "proc_pidfdinfo")
	})
	return libprocErr
}

func darwinVnodePath(data []byte) string {
	var best string
	for i, b := range data {
		if b != '/' {
			continue
		}
		rest := data[i:]
		end := bytes.IndexByte(rest, 0)
		if end < 0 {
			continue
		}
		candidate := string(rest[:end])
		if len(candidate) <= len(best) || !filepath.IsAbs(candidate) || !utf8.ValidString(candidate) {
			continue
		}
		printable := true
		for _, r := range candidate {
			if r < 0x20 || r == 0x7f {
				printable = false
				break
			}
		}
		if printable {
			best = candidate
		}
	}
	return best
}
