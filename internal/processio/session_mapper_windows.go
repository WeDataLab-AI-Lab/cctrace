//go:build windows

package processio

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

type systemHandleInfoHeader struct {
	Count    uintptr
	Reserved uintptr
}

type systemHandleEntry struct {
	Object                uintptr
	ProcessID             uintptr
	HandleValue           uintptr
	GrantedAccess         uint32
	CreatorBackTraceIndex uint16
	ObjectTypeIndex       uint16
	HandleAttributes      uint32
	Reserved              uint32
}

func checkOpenFileMappingSupport() error {
	if _, err := processOpenFiles(os.Getpid()); err != nil {
		return errors.New("Windows process-handle inspection is unavailable; safe Codex session mapping cannot run")
	}
	return nil
}

func processStartToken(pid int) (string, error) {
	if pid <= 0 {
		return "", errors.New("invalid process id")
	}
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(process)
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(process, &created, &exited, &kernel, &user); err != nil {
		return "", err
	}
	start := uint64(created.HighDateTime)<<32 | uint64(created.LowDateTime)
	return fmt.Sprintf("windows:%d", start), nil
}

func matchesClaudeProcStart(pid int, procStart, procStartFt string) bool {
	if procStartFt == "" || procStart != "" {
		return false
	}
	got, err := claudeProcStartForPID(pid)
	if err != nil {
		return false
	}
	return got == procStartFt
}

func claudeProcStartForPID(pid int) (string, error) {
	token, err := processStartToken(pid)
	if err != nil {
		return "", err
	}
	return strings.TrimPrefix(token, "windows:"), nil
}

func processOpenFiles(pid int) ([]string, error) {
	if pid <= 0 {
		return nil, errors.New("invalid process id")
	}
	process, err := windows.OpenProcess(windows.PROCESS_DUP_HANDLE, false, uint32(pid))
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(process)
	current, err := windows.GetCurrentProcess()
	if err != nil {
		return nil, err
	}
	info, err := systemHandleTable()
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, 4)
	for _, handle := range info {
		if handle.ProcessID != uintptr(pid) {
			continue
		}
		var duplicate windows.Handle
		if err := windows.DuplicateHandle(process, windows.Handle(handle.HandleValue), current, &duplicate, 0, false, windows.DUPLICATE_SAME_ACCESS); err != nil {
			continue
		}
		if fileType, err := windows.GetFileType(duplicate); err == nil && fileType == windows.FILE_TYPE_DISK {
			if path, err := finalPath(duplicate); err == nil {
				paths = append(paths, path)
			}
		}
		_ = windows.CloseHandle(duplicate)
	}
	return paths, nil
}

func systemHandleTable() ([]systemHandleEntry, error) {
	size := uint32(1 << 20)
	const maximumSize = 64 << 20
	var buffer []byte
	for {
		buffer = make([]byte, size)
		var returned uint32
		err := windows.NtQuerySystemInformation(windows.SystemExtendedHandleInformation,
			unsafe.Pointer(&buffer[0]), size, &returned)
		if err == nil {
			break
		}
		if returned > size {
			size = returned + (64 << 10)
		} else if size < maximumSize {
			size *= 2
		} else {
			return nil, err
		}
		if size > maximumSize {
			return nil, errors.New("Windows handle table exceeds the inspection limit")
		}
	}
	headerSize := unsafe.Sizeof(systemHandleInfoHeader{})
	entrySize := unsafe.Sizeof(systemHandleEntry{})
	header := (*systemHandleInfoHeader)(unsafe.Pointer(&buffer[0]))
	if header.Count > uintptr((len(buffer)-int(headerSize))/int(entrySize)) {
		return nil, errors.New("Windows handle table response is malformed")
	}
	entries := make([]systemHandleEntry, int(header.Count))
	base := uintptr(unsafe.Pointer(&buffer[0])) + headerSize
	for i := range entries {
		entries[i] = *(*systemHandleEntry)(unsafe.Pointer(base + uintptr(i)*entrySize))
	}
	return entries, nil
}

func finalPath(handle windows.Handle) (string, error) {
	buffer := make([]uint16, 32768)
	n, err := windows.GetFinalPathNameByHandle(handle, &buffer[0], uint32(len(buffer)), 0)
	if err != nil || n == 0 || n >= uint32(len(buffer)) {
		return "", errors.New("open file path is unavailable")
	}
	path := windows.UTF16ToString(buffer[:n])
	if strings.HasPrefix(path, `\\?\UNC\`) {
		path = `\\` + strings.TrimPrefix(path, `\\?\UNC\`)
	} else {
		path = strings.TrimPrefix(path, `\\?\`)
	}
	return filepath.Clean(path), nil
}
