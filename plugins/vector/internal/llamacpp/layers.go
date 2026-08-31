package llamacpp

import (
	"runtime"
	"strings"
)

// acceleratorLayers is "put the whole model on the accelerator": llama.cpp
// clamps the count to the layers the model actually has.
const acceleratorLayers = 99

// Occasion is why an engine is being opened. It is the difference between a
// background pass nobody is watching and a bulk build someone is waiting on.
type Occasion string

const (
	// OccasionRead is a live query or the resident reader.
	OccasionRead Occasion = "read"
	// OccasionDelta is background incremental ingest, next to a live session.
	OccasionDelta Occasion = "delta"
	// OccasionBulk is a from-scratch install or a reembed rebuild.
	OccasionBulk Occasion = "bulk"
)

// Lever is the operator's explicit backend choice. Unset means "use the
// default for this occasion"; the other two override it in either direction.
type Lever string

const (
	LeverUnset      Lever = ""
	LeverAccelerate Lever = "accelerate"
	LeverCPU        Lever = "cpu"
)

// Policy answers which backend an engine takes, and why, from the occasion and
// the operator lever. Defaults per occasion: readers and bulk builds take the
// accelerator, background delta ingest leaves it to live search. An explicit
// lever wins over the default in either direction.
type Policy struct {
	Occasion Occasion
	Lever    Lever
}

// ReadPolicy is the reader seat: live query and the resident process.
func ReadPolicy() Policy {
	return Policy{Occasion: OccasionRead}
}

// WriterOccasion separates a bulk build (install, reembed) from the background
// delta pass that keeps the index current.
func WriterOccasion(bulk bool) Occasion {
	if bulk {
		return OccasionBulk
	}
	return OccasionDelta
}

// LeverFor turns a boolean the operator actually typed into a lever.
func LeverFor(accelerate bool) Lever {
	if accelerate {
		return LeverAccelerate
	}
	return LeverCPU
}

// LeverFrom reads the lever out of an environment value. Anything it does not
// recognize, blank included, leaves the occasion's default in charge.
func LeverFrom(value string) Lever {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return LeverAccelerate
	case "0", "false", "no", "off":
		return LeverCPU
	default:
		return LeverUnset
	}
}

// GPULayers chooses how many model layers run on the accelerator.
func (p Policy) GPULayers() int {
	return p.layers(acceleratorPresent())
}

// Reason is the sentence telemetry records next to the backend, so a run that
// went to the CPU, or took the accelerator, says which decision put it there.
func (p Policy) Reason() string {
	return p.reason(acceleratorPresent())
}

func (p Policy) layers(accelerator bool) int {
	if accelerator && p.accelerates() {
		return acceleratorLayers
	}
	return 0
}

func (p Policy) accelerates() bool {
	switch p.Lever {
	case LeverAccelerate:
		return true
	case LeverCPU:
		return false
	}
	return p.Occasion == OccasionRead || p.Occasion == OccasionBulk
}

// A machine with no accelerator has no decision to report: every occasion runs
// on the CPU and there is nothing a reader of the log could act on.
func (p Policy) reason(accelerator bool) string {
	if !accelerator {
		return ""
	}
	switch {
	case p.Lever == LeverAccelerate:
		return "operator requested accelerator"
	case p.Lever == LeverCPU:
		return "operator requested cpu"
	case p.Occasion == OccasionBulk:
		return "bulk build default"
	case p.Occasion == OccasionDelta:
		return "indexing leaves the accelerator for live search"
	default:
		return ""
	}
}

func acceleratorPresent() bool {
	return runtime.GOOS == "darwin"
}
