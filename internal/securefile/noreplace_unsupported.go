//go:build !darwin && !linux && !windows

package securefile

func renameNoReplace(_, _ string) error {
	return errAtomicNoReplaceUnsupported
}
