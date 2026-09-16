//go:build windows

package plugin

import (
	"os"

	"golang.org/x/sys/windows"
)

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
