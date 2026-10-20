//go:build windows

package profile

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"

	"cctrace/internal/filesec"
)

func validateCodexHomeOwnerAndMode(path string, _ os.FileInfo) error {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil || sd == nil {
		return errors.New("cannot inspect codex_home security")
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return errors.New("cannot inspect codex_home owner")
	}
	current, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || !owner.Equals(current.User.Sid) {
		return errors.New("codex_home must be owned by the current user")
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil {
		return errors.New("codex_home must have a restrictive access list")
	}
	if filesec.GrantsBroadWrite(acl) {
		return errors.New("codex_home grants write access to a broad user group")
	}
	return nil
}

func DirectoryIdentity(path string) (string, error) {
	if err := CheckCodexHome(path); err != nil {
		return "", err
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	var info windows.ByHandleFileInformation
	if err = windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &info); err != nil {
		return "", err
	}
	return fmt.Sprintf("%d:%d:%d", info.VolumeSerialNumber, info.FileIndexHigh, info.FileIndexLow), nil
}
