//go:build windows

package store

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

type snapshotWindowsFileBasicInfo struct {
	creationTime   int64
	lastAccessTime int64
	lastWriteTime  int64
	changeTime     int64
	attributes     uint32
}

func snapshotFileIdentity(path string, _ os.FileInfo) (string, error) {
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
	var basic snapshotWindowsFileBasicInfo
	if err := windows.GetFileInformationByHandleEx(handle, windows.FileBasicInfo,
		(*byte)(unsafe.Pointer(&basic)), uint32(unsafe.Sizeof(basic))); err != nil {
		return "", err
	}
	return fmt.Sprintf("%d:%d:%d:%d", identity.VolumeSerialNumber,
		identity.FileIndexHigh, identity.FileIndexLow, basic.changeTime), nil
}
