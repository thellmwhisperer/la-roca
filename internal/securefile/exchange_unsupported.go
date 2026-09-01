//go:build !darwin && !linux && !windows

package securefile

func exchange(_, _ string) error {
	return errAtomicExchangeUnsupported
}
