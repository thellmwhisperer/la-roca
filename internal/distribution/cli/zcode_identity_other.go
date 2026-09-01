//go:build !windows && !linux

package cli

func zcodeRootIdentity(path string) (string, error) {
	return zcodeUnixRootIdentity(path)
}
