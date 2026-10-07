//go:build !linux && !darwin

package incrementality

import "os"

func fileChangeIdentity(info os.FileInfo) string {
	return ""
}
