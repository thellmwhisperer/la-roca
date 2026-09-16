//go:build windows

package plugin

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

func openOperatorRide(path string) (*os.File, error) {
	return os.Open(path)
}

func operatorRideFilePermissionsAllowed(file *os.File, _ os.FileInfo) error {
	if file == nil {
		return os.ErrInvalid
	}
	securityDescriptor, err := windows.GetSecurityInfo(
		windows.Handle(file.Fd()),
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		return err
	}
	if securityDescriptor == nil {
		return fmt.Errorf("has no security descriptor")
	}
	owner, _, err := securityDescriptor.Owner()
	if err != nil {
		return err
	}
	if owner == nil {
		return fmt.Errorf("has no owner")
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	if user == nil || user.User.Sid == nil {
		return fmt.Errorf("current user is unavailable")
	}
	dacl, _, err := securityDescriptor.DACL()
	if err == windows.ERROR_OBJECT_NOT_FOUND {
		return fmt.Errorf("has no DACL")
	}
	if err != nil {
		return err
	}
	if dacl == nil {
		return fmt.Errorf("has a permissive or missing DACL; refuse to run its rides")
	}
	systemSID, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return err
	}
	administratorsSID, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return err
	}
	const writeMask = uint32(windows.FILE_WRITE_DATA | windows.FILE_APPEND_DATA |
		windows.FILE_WRITE_EA | windows.FILE_WRITE_ATTRIBUTES | windows.DELETE |
		windows.WRITE_DAC | windows.WRITE_OWNER | windows.GENERIC_WRITE | windows.GENERIC_ALL)
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, index, &ace); err != nil {
			return err
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			continue
		}
		if uint32(ace.Mask)&writeMask == 0 {
			continue
		}
		aceSID := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if aceSID.Equals(owner) || aceSID.Equals(user.User.Sid) ||
			aceSID.Equals(systemSID) || aceSID.Equals(administratorsSID) {
			continue
		}
		return fmt.Errorf("is writable by group or others; refuse to run its rides")
	}
	return nil
}

func operatorRideFileOwnedByUser(file *os.File, _ os.FileInfo, _ int) (bool, error) {
	if file == nil {
		return false, os.ErrInvalid
	}
	securityDescriptor, err := windows.GetSecurityInfo(
		windows.Handle(file.Fd()),
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION,
	)
	if err != nil {
		return false, err
	}
	owner, _, err := securityDescriptor.Owner()
	if err != nil {
		return false, err
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return false, err
	}
	if owner == nil || user == nil || user.User.Sid == nil {
		return false, os.ErrInvalid
	}
	return owner.Equals(user.User.Sid), nil
}
