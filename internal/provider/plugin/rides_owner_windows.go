//go:build windows

package plugin

import "os"

func fileOwnerUID(os.FileInfo) (int, bool) { return 0, false }
