//go:build cgo && !windows

package llamacpp

/*
#cgo CXXFLAGS: -std=c++17 -O3 -I${SRCDIR}/../../../../.tmp/llama.cpp/include -I${SRCDIR}/../../../../.tmp/llama.cpp/ggml/include -I${SRCDIR}/../../../../.tmp/llama.cpp/src
#include "wrapper.h"
#include <stdlib.h>
*/
import "C"

import (
	"fmt"
	"runtime"
	"unsafe"
)

const (
	BackendMetal = "metal"
	BackendCPU   = "cpu"
)

func RequestAbort() { C.roca_llama_request_abort() }

func ClearAbort() { C.roca_llama_clear_abort() }

type Engine struct {
	engine         *C.roca_llama_engine
	Backend        string
	FallbackReason string
}

func Open(model string, threads, gpuLayers int) (*Engine, error) {
	if threads <= 0 {
		threads = runtime.NumCPU()
	}
	ClearAbort()
	path := C.CString(model)
	defer C.free(unsafe.Pointer(path))
	var message *C.char
	var accelerated C.int
	engine := C.roca_llama_open(path, C.int(threads), C.int(gpuLayers), &accelerated, &message)
	if engine == nil {
		defer C.roca_llama_release(unsafe.Pointer(message))
		return nil, fmt.Errorf("open the embedding model: %s", C.GoString(message))
	}
	backend, fallback := selectedBackend(gpuLayers, accelerated != 0)
	result := &Engine{engine: engine, Backend: backend, FallbackReason: fallback}
	return result, nil
}

func selectedBackend(gpuLayers int, accelerated bool) (string, string) {
	if gpuLayers <= 0 {
		return BackendCPU, ""
	}
	if accelerated {
		return BackendMetal, ""
	}
	return BackendCPU, "accelerator unavailable"
}

// OpenPreferred opens the engine the way the policy asks and falls back to the
// CPU rather than failing outright when the accelerator will not start.
func OpenPreferred(model string, threads int, policy Policy) (*Engine, error) {
	layers := policy.GPULayers()
	engine, err := Open(model, threads, layers)
	if err == nil || layers == 0 {
		return engine, err
	}
	cpu, cpuErr := Open(model, threads, 0)
	if cpuErr != nil {
		return nil, err
	}
	cpu.FallbackReason = "accelerator init failed"
	return cpu, nil
}

func (e *Engine) Close() {
	if e != nil && e.engine != nil {
		C.roca_llama_close(e.engine)
		e.engine = nil
	}
}

func (e *Engine) Embed(text string) ([]float32, int, error) {
	ClearAbort()
	input := C.CString(text)
	defer C.free(unsafe.Pointer(input))
	var raw *C.float
	var dimensions C.int
	var tokens C.int
	var message *C.char
	if C.roca_llama_embed(e.engine, input, C.size_t(len(text)), &raw, &dimensions, &tokens, &message) != 0 {
		defer C.roca_llama_release(unsafe.Pointer(message))
		return nil, 0, fmt.Errorf("embed: %s", C.GoString(message))
	}
	defer C.roca_llama_release(unsafe.Pointer(raw))
	vector := make([]float32, int(dimensions))
	copy(vector, unsafe.Slice((*float32)(unsafe.Pointer(raw)), int(dimensions)))
	return vector, int(tokens), nil
}
