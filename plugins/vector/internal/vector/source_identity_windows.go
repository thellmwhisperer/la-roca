package vector

import (
	"encoding/binary"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// ChangeTime, unlike LastWriteTime, detects a timestamp-preserving restore.
func fileChangeIdentity(path string, _ os.FileInfo) (string, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", err
	}
	handle, err := windows.CreateFile(name, windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(handle)
	var identity windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &identity); err != nil {
		return "", err
	}
	// FILE_BASIC_INFO: four 64-bit times, attributes and alignment padding.
	var basic [40]byte
	if err := windows.GetFileInformationByHandleEx(handle, windows.FileBasicInfo, &basic[0], uint32(len(basic))); err != nil {
		return "", err
	}
	return fmt.Sprintf("%d:%d:%d:%d", identity.VolumeSerialNumber,
		identity.FileIndexHigh, identity.FileIndexLow, binary.LittleEndian.Uint64(basic[24:32])), nil
}
