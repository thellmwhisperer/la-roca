//go:build windows

package securefile

import "golang.org/x/sys/windows"

func windowsPathPair(source, target string) (*uint16, *uint16, error) {
	from, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return nil, nil, err
	}
	to, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return nil, nil, err
	}
	return from, to, nil
}
