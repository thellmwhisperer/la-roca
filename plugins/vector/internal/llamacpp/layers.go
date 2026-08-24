package llamacpp

import "runtime"

// GPULayers chooses how many model layers run on the accelerator.
// Readers (live query / _resident) may take it. Writers (ingest / worker)
// stay on the host CPU so a live search session cannot stall indexing, and
// indexing cannot stall live search, when both load the same engine.
func GPULayers(readOnly bool) int {
	if readOnly && runtime.GOOS == "darwin" {
		return 99
	}
	return 0
}
