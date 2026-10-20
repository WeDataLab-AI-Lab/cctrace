//go:build windows

package filesec

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// unsafeWriteMask is every right that lets a holder change what the object
// holds or who may: generic all and write, write data, append data, delete
// child, delete, write DACL and write owner.
const unsafeWriteMask = windows.ACCESS_MASK(0x10000000 | 0x40000000 | 0x00000002 | 0x00000004 | 0x00000040 | 0x00010000 | 0x00040000 | 0x00080000)

// GrantsBroadWrite reports whether acl has an allow entry with a write-class
// right for Everyone, Authenticated Users or BUILTIN\Users. It looks for those
// three SIDs and no others: a grant to a named user, to INTERACTIVE, to Guests
// or to Domain Users is not detected. Deny entries and callback or conditional
// allow entries are not read.
func GrantsBroadWrite(acl *windows.ACL) bool {
	wideSIDs := make([]*windows.SID, 0, 3)
	for _, kind := range []windows.WELL_KNOWN_SID_TYPE{windows.WinWorldSid, windows.WinAuthenticatedUserSid, windows.WinBuiltinUsersSid} {
		sid, err := windows.CreateWellKnownSid(kind)
		if err == nil {
			wideSIDs = append(wideSIDs, sid)
		}
	}
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, i, &ace); err != nil || ace == nil || ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Mask&unsafeWriteMask == 0 {
			continue
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		for _, wide := range wideSIDs {
			if sid.Equals(wide) {
				return true
			}
		}
	}
	return false
}
