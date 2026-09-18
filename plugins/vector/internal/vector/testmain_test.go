package vector

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	if RunWatchdogSleepIfRequested(os.Args) {
		os.Exit(0)
	}
	os.Exit(m.Run())
}
