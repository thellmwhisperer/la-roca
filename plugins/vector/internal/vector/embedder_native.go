//go:build !windows

package vector

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"

	"github.com/thellmwhisperer/la-roca-vector/internal/engine"
	"github.com/thellmwhisperer/la-roca-vector/internal/llamacpp"
	"github.com/thellmwhisperer/la-roca-vector/internal/model"
	"github.com/thellmwhisperer/la-roca-vector/internal/telemetry"
)

// Native is the unix embeddings engine: one downloaded model file, no daemon.
type Native struct {
	DataDir   string
	StateDir  string
	Events    engine.Sink
	Telemetry *telemetry.Store
	ReadOnly  bool
	// Writer is the backend policy for an indexing run: which occasion this
	// pass is, plus whatever lever the operator pulled. Readers ignore it.
	Writer        llamacpp.Policy
	ownershipOnce sync.Once
	ownership     chan struct{}
	engine        nativeEngine
	backend       string
	fallback      string
	terminal      atomic.Pointer[nativeTrap]
	activeElement atomic.Pointer[string]
	trapAction    func(string) error
}

type nativeTrap struct {
	err error
}

const (
	nativeTrapElementEnv  = "ROCA_VECTOR_NATIVE_TRAP_ELEMENT"
	nativeTrapRestartsEnv = "ROCA_VECTOR_NATIVE_TRAP_RESTARTS"
	maxNativeTrapRestarts = 1
)

var (
	errNativeTrapped     = fmt.Errorf("semantic search stalled while preparing embeddings")
	execWorkerProcess    = syscall.Exec
	restartTrappedWorker = restartTrappedWorkerProcess
)

func restartTrappedWorkerProcess(element string) error {
	restarts := 0
	if os.Getenv(nativeTrapElementEnv) == element {
		var err error
		restarts, err = strconv.Atoi(os.Getenv(nativeTrapRestartsEnv))
		if err != nil || restarts < 0 {
			return nativeTrapBudgetError(element, maxNativeTrapRestarts)
		}
	}
	if restarts >= maxNativeTrapRestarts {
		return nativeTrapBudgetError(element, restarts)
	}
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("restart vector worker after native stall: %w", err)
	}
	environment := replaceEnvironment(os.Environ(), nativeTrapElementEnv, element)
	environment = replaceEnvironment(environment, nativeTrapRestartsEnv, strconv.Itoa(restarts+1))
	if err := execWorkerProcess(executable, os.Args, environment); err != nil {
		return fmt.Errorf("restart vector worker after native stall: %w", err)
	}
	return nil
}

func nativeTrapBudgetError(element string, restarts int) error {
	return fmt.Errorf("%w: embedding element %s stalled after %d worker restart", errNativeTrapped,
		element, restarts)
}

func replaceEnvironment(environment []string, key, value string) []string {
	prefix := key + "="
	result := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		if !strings.HasPrefix(entry, prefix) {
			result = append(result, entry)
		}
	}
	return append(result, prefix+value)
}

func nativeElementIdentity(input string) string {
	digest := sha256.Sum256([]byte(input))
	return fmt.Sprintf("sha256:%x", digest[:8])
}

type nativeEngine interface {
	Embed(string) ([]float32, int, error)
	Close()
}

func (n *Native) acquireNative(ctx context.Context) error {
	if err := n.TerminalError(); err != nil {
		return err
	}
	n.ownershipOnce.Do(func() {
		n.ownership = make(chan struct{}, 1)
	})
	select {
	case n.ownership <- struct{}{}:
		if err := n.TerminalError(); err != nil {
			<-n.ownership
			return err
		}
		if err := ctx.Err(); err != nil {
			<-n.ownership
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (n *Native) markNativeTrapped(element string) {
	state := &nativeTrap{err: errNativeTrapped}
	if !n.terminal.CompareAndSwap(nil, state) {
		return
	}
	if n.trapAction != nil {
		if err := n.trapAction(element); err != nil {
			n.terminal.Store(&nativeTrap{err: err})
		}
	}
}

func (n *Native) TerminalError() error {
	if state := n.terminal.Load(); state != nil {
		return state.err
	}
	return nil
}

func (n *Native) trappedElement(input []string) string {
	if active := n.activeElement.Load(); active != nil {
		return *active
	}
	if len(input) > 0 {
		return nativeElementIdentity(input[0])
	}
	return nativeElementIdentity("")
}

func EnableWorkerRestartOnNativeTrap(embedder Embedder) {
	if native, ok := embedder.(*Native); ok {
		native.trapAction = restartTrappedWorker
	}
}

func (n *Native) releaseNative() {
	<-n.ownership
}

func (n *Native) nativeContextError(callerCtx context.Context) error {
	if err := callerCtx.Err(); err != nil {
		return err
	}
	n.record(telemetry.Record{Kind: telemetry.KindError, Err: "semantic search stalled"})
	if err := n.TerminalError(); err != nil {
		return err
	}
	return errNativeTrapped
}

func ConfiguredEmbedder(dataDir, stateDir string, events engine.Sink, tel *telemetry.Store,
	readOnly bool, writer llamacpp.Policy) Embedder {
	return &Native{DataDir: dataDir, StateDir: stateDir, Events: events, Telemetry: tel,
		ReadOnly: readOnly, Writer: writer}
}

// writerFallbackReason keeps the engine's own answer when it has one: an
// accelerator that refused to start is a better explanation of a CPU run than
// the policy that asked for it.
func writerFallbackReason(policy, existing string) string {
	if existing != "" {
		return existing
	}
	return policy
}

func (n *Native) Pull(ctx context.Context, requestedModel string) error {
	return n.pull(ctx, requestedModel)
}

func (n *Native) pull(ctx context.Context, requestedModel string) error {
	if requestedModel != DefaultModel {
		return fmt.Errorf("embedding model %q is not supported by this engine", requestedModel)
	}
	_, err := n.modelPath(ctx)
	return err
}

func (n *Native) modelPath(ctx context.Context) (string, error) {
	if n.ReadOnly {
		return model.Existing(n.DataDir, model.DefaultManifest())
	}
	return model.Ensure(ctx, n.DataDir, model.DefaultManifest(), n.Events)
}

func (n *Native) record(record telemetry.Record) {
	if n.Telemetry == nil {
		return
	}
	_ = n.Telemetry.Record(context.Background(), record)
}

func (n *Native) emit(event engine.Event) {
	if n.Events != nil {
		n.Events(event)
	}
}

func memoryHighWater() int64 {
	return processHighWater()
}
