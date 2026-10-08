package service

import (
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/store/search"
)

func TestTimedOutSurfaceOutranksAnotherSurfaceFault(t *testing.T) {
	timedOut := search.Proof{TimedOut: true, Reason: "query timeout"}
	fastFault := search.Proof{Reason: "index did not answer"}

	fault := preferWordSearchFault(nil, timedOut)
	fault = preferWordSearchFault(fault, fastFault)
	if fault == nil || !fault.TimedOut {
		t.Fatalf("two-surface proof = %+v, want timeout", fault)
	}

	fault = preferWordSearchFault(nil, fastFault)
	fault = preferWordSearchFault(fault, timedOut)
	if fault == nil || !fault.TimedOut {
		t.Fatalf("reversed two-surface proof = %+v, want timeout", fault)
	}
}
