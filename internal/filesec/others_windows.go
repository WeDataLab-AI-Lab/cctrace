//go:build windows

package filesec

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// WritableByOthers reports whether the access list of the file or directory at
// path allows Everyone, Authenticated Users or BUILTIN\Users a write-class right
// (GrantsBroadWrite). Those three groups are all it looks for: a grant to a named
// user, to INTERACTIVE, to Guests or to Domain Users is not detected, the same
// rule internal/profile applies to the Codex home. Go reports every directory as
// 0777 here, so the permission bits say nothing; info is not consulted.
//
// It does not require the owner to be the current user: a file an elevated
// process created belongs to BUILTIN\Administrators and is still the user's
// own. A file with no access list at all is open to everyone; that comes back
// as an error, which a caller must treat as "not safe", never as "no".
func WritableByOthers(path string, _ os.FileInfo) (bool, error) {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return false, fmt.Errorf("inspect file security: %w", err)
	}
	if sd == nil {
		return false, errors.New("inspect file security: no security descriptor")
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return false, fmt.Errorf("read file access list: %w", err)
	}
	if acl == nil {
		return false, errors.New("file has no access list")
	}
	return GrantsBroadWrite(acl), nil
}
