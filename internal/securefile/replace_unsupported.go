//go:build !darwin && !linux && !windows

package securefile

func renameReplace(_, _ string) error {
	return errAtomicReplaceUnsupported
}
