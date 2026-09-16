//go:build windows

package plugin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestOperatorRideFileDACL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rides.toml")
	if err := os.WriteFile(path, []byte(`[ride.backup]
command = "echo backup"
`), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	world, err := windows.CreateWellKnownSid(windows.WinWorldSid)
	if err != nil {
		t.Fatal(err)
	}

	setDACL := func(entries []windows.EXPLICIT_ACCESS) {
		t.Helper()
		acl, err := windows.ACLFromEntries(entries, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := windows.SetSecurityInfo(
			windows.Handle(file.Fd()),
			windows.SE_FILE_OBJECT,
			windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
			nil,
			nil,
			acl,
			nil,
		); err != nil {
			t.Fatal(err)
		}
	}
	ownerEntry := func(mask windows.ACCESS_MASK) windows.EXPLICIT_ACCESS {
		return windows.EXPLICIT_ACCESS{
			AccessPermissions: mask,
			AccessMode:        windows.GRANT_ACCESS,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_USER,
				TrusteeValue: windows.TrusteeValueFromSID(user.User.Sid),
			},
		}
	}
	worldEntry := func(mask windows.ACCESS_MASK) windows.EXPLICIT_ACCESS {
		return windows.EXPLICIT_ACCESS{
			AccessPermissions: mask,
			AccessMode:        windows.GRANT_ACCESS,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_GROUP,
				TrusteeValue: windows.TrusteeValueFromSID(world),
			},
		}
	}

	setDACL([]windows.EXPLICIT_ACCESS{ownerEntry(windows.FILE_GENERIC_READ | windows.FILE_GENERIC_WRITE)})
	if rides, warnings, err := DiscoverOperatorRides(path, ""); err != nil ||
		len(rides) != 1 || len(warnings) != 0 {
		t.Fatalf("owner-only DACL: rides=%+v warnings=%v err=%v", rides, warnings, err)
	}
	setDACL([]windows.EXPLICIT_ACCESS{
		ownerEntry(windows.FILE_GENERIC_READ | windows.FILE_GENERIC_WRITE),
		worldEntry(windows.FILE_GENERIC_READ),
	})
	if rides, warnings, err := DiscoverOperatorRides(path, ""); err != nil ||
		len(rides) != 1 || len(warnings) != 0 {
		t.Fatalf("world-read DACL: rides=%+v warnings=%v err=%v", rides, warnings, err)
	}
	setDACL([]windows.EXPLICIT_ACCESS{
		ownerEntry(windows.FILE_GENERIC_READ | windows.FILE_GENERIC_WRITE),
		worldEntry(windows.FILE_GENERIC_WRITE),
	})
	if _, _, err := DiscoverOperatorRides(path, ""); err == nil ||
		!strings.Contains(err.Error(), "writable by group or others") {
		t.Fatalf("world-write DACL: %v", err)
	}
	if err := windows.SetSecurityInfo(
		windows.Handle(file.Fd()),
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil,
		nil,
		nil,
		nil,
	); err != nil {
		t.Fatal(err)
	}
	if _, _, err := DiscoverOperatorRides(path, ""); err == nil {
		t.Fatalf("null DACL: %v", err)
	}
}
