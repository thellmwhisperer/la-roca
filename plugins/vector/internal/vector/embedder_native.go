//go:build !windows

package vector

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
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
	nativeTrapDeathsEnv     = "ROCA_VECTOR_NATIVE_TRAP_DEATHS"
	maxNativeTrapRestarts   = 1
	maxNativeTrapLedgerSize = 64
)

var (
	errNativeTrapped  = fmt.Errorf("semantic search stalled while preparing embeddings")
	execWorkerProcess = syscall.Exec
)

type WorkerTrapRecovery struct {
	cancel      context.CancelFunc
	drain       func()
	mu          sync.Mutex
	environment []string
}

func NewWorkerTrapRecovery(cancel context.CancelFunc, drain func()) *WorkerTrapRecovery {
	return &WorkerTrapRecovery{cancel: cancel, drain: drain}
}

func (r *WorkerTrapRecovery) Handle(element string) error {
	environment, err := nativeTrapRestartEnvironment(element, os.Environ())
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.environment = environment
	r.mu.Unlock()
	r.cancel()
	return nil
}

func (r *WorkerTrapRecovery) RestartIfRequested() (bool, error) {
	r.mu.Lock()
	environment := r.environment
	r.mu.Unlock()
	if environment == nil {
		return false, nil
	}
	r.drain()
	executable, err := os.Executable()
	if err != nil {
		return true, fmt.Errorf("restart vector worker after native stall: %w", err)
	}
	if err := execWorkerProcess(executable, os.Args, environment); err != nil {
		return true, fmt.Errorf("restart vector worker after native stall: %w", err)
	}
	return true, nil
}

func nativeTrapRestartEnvironment(element string, environment []string) ([]string, error) {
	deaths := map[string]int{}
	if encoded := environmentValue(environment, nativeTrapDeathsEnv); encoded != "" {
		if err := json.Unmarshal([]byte(encoded), &deaths); err != nil || deaths == nil {
			return nil, nativeTrapBudgetError(element, maxNativeTrapRestarts)
		}
	}
	for _, count := range deaths {
		if count < 0 || count > maxNativeTrapRestarts {
			return nil, nativeTrapBudgetError(element, maxNativeTrapRestarts)
		}
	}
	if deaths[element] >= maxNativeTrapRestarts {
		return nil, nativeTrapBudgetError(element, deaths[element])
	}
	if _, known := deaths[element]; !known && len(deaths) >= maxNativeTrapLedgerSize {
		return nil, fmt.Errorf("%w: native restart ledger is full at embedding element %s",
			errNativeTrapped, element)
	}
	deaths[element]++
	encoded, err := json.Marshal(deaths)
	if err != nil {
		return nil, fmt.Errorf("record native restart state: %w", err)
	}
	return replaceEnvironment(environment, nativeTrapDeathsEnv, string(encoded)), nil
}

func nativeTrapBudgetError(element string, restarts int) error {
	return fmt.Errorf("%w: embedding element %s stalled after %d worker restart", errNativeTrapped,
		element, restarts)
}

func environmentValue(environment []string, key string) string {
	prefix := key + "="
	for _, entry := range environment {
		if strings.HasPrefix(entry, prefix) {
			return strings.TrimPrefix(entry, prefix)
		}
	}
	return ""
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

func EnableWorkerRestartOnNativeTrap(embedder Embedder, action func(string) error) {
	if native, ok := embedder.(*Native); ok {
		native.trapAction = action
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
