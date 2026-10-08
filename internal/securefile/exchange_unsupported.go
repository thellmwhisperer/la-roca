//go:build !darwin && !linux

package securefile

func exchange(_, _ string) error {
	return errAtomicExchangeUnsupported
}
