//go:build cgo && linux

package llamacpp

/*
#cgo LDFLAGS: -L${SRCDIR}/../../../../.tmp/llama.cpp/build-native/src -lllama -L${SRCDIR}/../../../../.tmp/llama.cpp/build-native/ggml/src -lggml -lggml-cpu -lggml-base -lstdc++ -lm -lpthread -ldl
*/
import "C"
