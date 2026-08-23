//go:build cgo && darwin

package llamacpp

/*
#cgo LDFLAGS: -L${SRCDIR}/../../../../.tmp/llama.cpp/build-native/src -lllama -L${SRCDIR}/../../../../.tmp/llama.cpp/build-native/ggml/src -lggml -lggml-cpu -L${SRCDIR}/../../../../.tmp/llama.cpp/build-native/ggml/src/ggml-blas -lggml-blas -L${SRCDIR}/../../../../.tmp/llama.cpp/build-native/ggml/src/ggml-metal -lggml-metal -lggml-base -framework Accelerate -framework Foundation -framework Metal -framework MetalKit -lc++ -lm
*/
import "C"
