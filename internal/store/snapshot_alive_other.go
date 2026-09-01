//go:build !unix && !windows

package store

func pidExists(int) (bool, error) {
	return false, errSnapshotOwnerUncertain
}
