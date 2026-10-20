//go:build windows

package filesectest

import (
	"runtime"
	"testing"

	"golang.org/x/sys/windows"
)

// Grant adds an allow entry for a well-known group to the access list of path,
// keeping every entry it already has.
func Grant(t testing.TB, path string, group windows.WELL_KNOWN_SID_TYPE, mask windows.ACCESS_MASK) {
	t.Helper()
	sid, err := windows.CreateWellKnownSid(group)
	if err != nil {
		t.Fatal(err)
	}
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	current, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	merged, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: mask,
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       windows.NO_INHERITANCE,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_WELL_KNOWN_GROUP,
			TrusteeValue: windows.TrusteeValueFromSID(sid),
		},
	}}, current)
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, merged, nil); err != nil {
		t.Fatal(err)
	}
	runtime.KeepAlive(sd)
}
