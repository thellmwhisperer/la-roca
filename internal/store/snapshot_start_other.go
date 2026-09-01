//go:build !darwin && !linux && !windows

package store

func processStartUnixNano(int) (int64, error) {
	return 0, errSnapshotStartUnknown
}
