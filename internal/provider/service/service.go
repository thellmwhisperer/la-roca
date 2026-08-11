// Package service is the kernel shared by the CLI and MCP surfaces.
package service

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/thellmwhisperer/la-roca/internal/ingest"
	"github.com/thellmwhisperer/la-roca/internal/provider"
	"github.com/thellmwhisperer/la-roca/internal/provider/layers"
	"github.com/thellmwhisperer/la-roca/internal/provider/query/sqlgate"
	"github.com/thellmwhisperer/la-roca/internal/store"
	"github.com/thellmwhisperer/la-roca/internal/store/search"
)

// DefaultMaxChars is the per-text-field budget shared by every service caller.
// A zero request means this default, never an unbounded response.
const DefaultMaxChars = 500

// Options are the service's opening options.
type Options struct {
	DBPath    string
	BackupDir string
	// DataDir is where personal artefacts hang next to the database. Empty falls
	// back to the directory of DBPath. Nothing generated from the operator's
	// data may ever be written outside it.
	DataDir string
	Version string
	Commit  string
	// QueryTimeout bounds execution after SQL passes the read-only gate. Zero
	// uses DefaultQueryTimeout.
	QueryTimeout time.Duration
	// ReadOnly refuses in the service, before any database I/O.
	ReadOnly bool
	// Providers is the resolved model cascade. Its zero value is a service that
	// contacts no provider, which is what an installation with no model
	// configured needs.
	Providers provider.Cascade
	// ConfigPath and ConfigExists are what doctor reports: every message about
	// configuration names the file, never a TOML table.
	ConfigPath   string
	ConfigExists bool
	// Sources is where every agent's artefacts live on this machine, already
	// resolved from the home, the environment and the configuration. It is
	// resolved by the surface and handed over, so this object never guesses at a
	// path of its own.
	Sources ingest.Roots
	// Progress receives terse human-readable phase lines. Structured surfaces
	// leave it nil and receive only the result.
	Progress func(string)
	// IngestProgress is the structured source counter used only by an
	// interactive shell renderer.
	IngestProgress func(ingest.SourceProgress)
}

// DefaultQueryTimeout prevents an accepted recursive or aggregate query from
// consuming resources indefinitely without requiring configuration.
const DefaultQueryTimeout = 5 * time.Second

// Service opens the database once and answers both surfaces.
type Service struct {
	db       *store.DB
	opts     Options
	registry layers.Registry

	gateOnce    sync.Once
	gate        *sqlgate.Gate
	gateFailure error
}

// Open opens the database without creating or adopting it: that is Init's job.
func Open(opts Options) (*Service, error) {
	registry, err := layers.Load()
	if err != nil {
		return nil, err
	}
	db, err := store.Open(opts.DBPath)
	if err != nil {
		return nil, err
	}
	return &Service{db: db, opts: opts, registry: registry}, nil
}

// DB exposes the database for whatever has no method of its own in the service
// yet.
func (s *Service) DB() *store.DB { return s.db }

// DataDir is the operator-owned directory where file traces live.
func (s *Service) DataDir() string { return s.dataDir() }

// Close closes the database.
func (s *Service) Close() error {
	if s.gate != nil {
		s.gate.Close()
	}
	return s.db.Close()
}

// theGate opens the read-only gate the first time it is needed. It is an
// in-memory database with the visible schema, so it costs a few milliseconds a
// command that does not query has no reason to pay.
func (s *Service) theGate() (*sqlgate.Gate, error) {
	s.gateOnce.Do(func() { s.gate, s.gateFailure = sqlgate.Open() })
	return s.gate, s.gateFailure
}

// InitResult is what init did with the database.
type InitResult struct {
	DBPath string `json:"-"`
	// ConfigPath is where this installation reads its settings from, whether or
	// not the file is there. An operator whose configuration is being ignored
	// needs to know which file the product looked at before they edit another.
	ConfigPath string         `json:"config_path"`
	Database   string         `json:"database"`
	Verdict    string         `json:"verdict"`
	Structures int            `json:"schema_structures"`
	Orphans    []string       `json:"orphans,omitempty"`
	Repairs    []string       `json:"repairs,omitempty"`
	BackupPath string         `json:"-"`
	Layers     int            `json:"layers"`
	Bytes      int64          `json:"database_bytes"`
	Rows       ingest.Tables  `json:"rows"`
	Search     *search.Report `json:"search_index,omitempty"`
	// Model and Ingest are the rest of the bootstrap: whether a model is going
	// to answer, and what the first read of the disk found. Neither can fail
	// the command, and both report.
	Model          *InitModel    `json:"model"`
	Ingest         *IngestResult `json:"ingest"`
	PromptPath     string        `json:"prompt_path"`
	Prompt         string        `json:"-"`
	Warnings       []string      `json:"warnings,omitempty"`
	RowsBefore     ingest.Tables `json:"-"`
	SetupElapsedMS int64         `json:"-"`
	ModelElapsedMS int64         `json:"-"`
	TotalElapsedMS int64         `json:"-"`
}

// InitModel is the model gate at bootstrap: which provider is going to answer,
// or why none is and what to do about it. It is the same verdict `roca doctor`
// prints, said once at the moment an operator first has a reason to care.
type InitModel struct {
	Ready    bool   `json:"ready"`
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	Reason   string `json:"reason,omitempty"`
	Action   string `json:"action,omitempty"`
	Disabled bool   `json:"disabled,omitempty"`
}

const presentationPrompt = "## La Roca — local semantic memory\n" +
	"La Roca contains local session history, curated memories, handoffs, decisions, " +
	"and tool traces from your agents.\n" +
	"when to query: at session start, before repeating research, and whenever prior " +
	"context or a decision may exist.\n" +
	"With a shell, use `roca query \"<natural question>\"`; preserve durable context " +
	"with `roca store`.\n" +
	"Data = `roca query`; human reading = `roca query --full`; raw SQL = `roca exec`.\n" +
	"Without a shell, use the MCP equivalents: `roca_query` and `roca_store`.\n" +
	"On first bootstrap, `roca init` asks new or adopt; adoption uses only the source path the user types.\n" +
	"La Roca never edits agent instruction files; a human chooses where to paste this block.\n"

// Init leaves the database ready: it creates the new one or adopts the one that
// is there, and resyncs the layer registry. It is idempotent by contract,
// because the real flow runs it more than once.
func (s *Service) Init(ctx context.Context) (InitResult, error) {
	started := time.Now()
	if s.opts.ReadOnly {
		return InitResult{}, errReadOnly
	}
	progress := s.opts.Progress
	if progress == nil {
		progress = func(string) {}
	}
	detected := ingest.DetectAgents(s.opts.Sources)
	progress("agents: checking known sources")
	progress("agents detected: " + strings.Join(detected, ", "))
	progress("database: inspecting " + s.db.Path())
	before, err := store.Inspect(ctx, s.db)
	if err != nil {
		return InitResult{}, err
	}
	adoption, err := store.Adopt(ctx, s.db, s.opts.BackupDir)
	if err != nil {
		return InitResult{}, err
	}
	if err := s.syncLayers(ctx); err != nil {
		return InitResult{}, err
	}
	state := "adopted"
	progressState := "existing"
	if before.Fresh {
		state = "created"
		progressState = "created"
	}
	rows := ingest.Tables{
		Memories: s.countOf(ctx, "memories"), Sessions: s.countOf(ctx, "sessions"),
		Exchanges: s.countOf(ctx, "exchanges"), ThinkingBlocks: s.countOf(ctx, "thinking_blocks"),
		ToolUses: s.countOf(ctx, "tool_uses"),
	}
	var bytes int64
	if info, statErr := os.Stat(s.db.Path()); statErr == nil {
		bytes = info.Size()
	}
	progress(fmt.Sprintf("database: %s · %d bytes · %d memories · %d sessions · %d exchanges",
		progressState, bytes, rows.Memories, rows.Sessions, rows.Exchanges))
	result := InitResult{
		DBPath:     s.db.Path(),
		ConfigPath: s.opts.ConfigPath,
		Database:   state,
		Verdict:    string(adoption.Verdict),
		Structures: adoption.RequiredStructures,
		Orphans:    adoption.Orphans,
		Repairs:    adoption.Repairs,
		BackupPath: adoption.BackupPath,
		Layers:     len(s.registry.Layers),
		Bytes:      bytes,
		Rows:       rows,
		RowsBefore: rows,
	}
	result.SetupElapsedMS = time.Since(started).Milliseconds()

	// The rest of the bootstrap. None of it may take the command down: a
	// database that is ready is the thing init promised, and a source that
	// cannot be read or a model that is not installed are reported states.
	progress("ingest: starting first read")
	result.Ingest = s.bootstrapIngest(ctx)
	progress(fmt.Sprintf("ingest: complete · %d files read · %d skipped · %d errors",
		result.Ingest.FilesRead, result.Ingest.FilesSkipped, result.Ingest.Errors))
	result.Search = result.Ingest.Index
	if result.Search != nil {
		progress(fmt.Sprintf("index: ready in %d ms", result.Search.ElapsedMS))
	}
	progress("model: checking declared providers")
	modelStarted := time.Now()
	result.Model = s.modelGate(ctx)
	result.ModelElapsedMS = time.Since(modelStarted).Milliseconds()
	if result.Model.Ready {
		progress("model: " + result.Model.Provider + "/" + result.Model.Model + " will answer")
	} else {
		progress("model: no provider will answer · " + result.Model.Reason)
	}
	result.Rows = result.Ingest.After
	if info, statErr := os.Stat(s.db.Path()); statErr == nil {
		result.Bytes = info.Size()
	}
	result.PromptPath = filepath.Join(s.dataDir(), "prompt.md")
	result.Prompt = presentationPrompt
	if err := os.WriteFile(result.PromptPath, []byte(result.Prompt), 0o600); err != nil {
		failedPath := result.PromptPath
		result.PromptPath = ""
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("write the agent prompt at %s: %v", failedPath, err))
	}
	result.TotalElapsedMS = time.Since(started).Milliseconds()
	return result, nil
}

// bootstrapIngest is init's first read of the disk. It is incremental like every other
// run, so on a machine that already ingested it costs a fingerprint check per
// file and writes nothing.
func (s *Service) bootstrapIngest(ctx context.Context) *IngestResult {
	report, err := s.Ingest(ctx, IngestRequest{})
	if err != nil {
		// An ingest that blows up is one more error in its own report, never a
		// bootstrap that fails: the database is ready and the operator can
		// query what was already there.
		report.Errors++
		report.ErrorDetails = append(report.ErrorDetails,
			ingest.Failure{Parser: "ingest", Reason: err.Error()})
	}
	return &report
}

// modelGate asks who is going to answer, without stopping at the first yes:
// the operator reading the bootstrap wants the picture, and a provider that is
// not available has to arrive with its remedy attached.
func (s *Service) modelGate(ctx context.Context) *InitModel {
	cascade := s.opts.Providers
	if cascade.Disabled {
		return &InitModel{Disabled: true, Reason: "the model is turned off in the configuration"}
	}
	if len(cascade.Providers) == 0 {
		return &InitModel{
			Reason: "no model provider is configured",
			Action: "declare one under [models] in " + s.opts.ConfigPath +
				", or run `roca doctor` to see the ones this version knows",
		}
	}
	gate := &InitModel{}
	for _, attempt := range cascade.Diagnose(ctx) {
		if attempt.Ready {
			return &InitModel{Ready: true, Provider: attempt.Name, Model: attempt.ModelID}
		}
		if gate.Reason == "" {
			gate.Provider, gate.Reason, gate.Action = attempt.Name, attempt.Reason, attempt.Action
		}
	}
	return gate
}

// dataDir is the directory the database hangs off. The agent prompt the
// bootstrap writes lives here, beside the operator's database.
func (s *Service) dataDir() string {
	if s.opts.DataDir != "" {
		return s.opts.DataDir
	}
	if s.opts.DBPath != "" {
		return filepath.Dir(s.opts.DBPath)
	}
	return ""
}

// countOf is how many rows a table holds, or zero when it cannot be asked.
//
// It is the live number `roca doctor` uses to distinguish an empty installation
// from a broken one. The table name is always a
// constant of this package, never anything that came in from outside: there is
// no interpolation of a caller's string here.
func (s *Service) countOf(ctx context.Context, table string) int {
	var count int
	row := s.db.SQL().QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table)
	if err := row.Scan(&count); err != nil {
		return 0
	}
	return count
}

// SchemaStatus classifies the database without touching it.
func (s *Service) SchemaStatus(ctx context.Context) (store.Report, error) {
	return store.Inspect(ctx, s.db)
}

var errReadOnly = fmt.Errorf("La Roca is in read-only mode: this operation writes")

// refuseReadOnly is that same refusal naming the operation it refused. The
// message belongs to the service and neither surface rewrites it: a shell and a
// plug that answer read-only mode with different words are two products.
func refuseReadOnly(operation string) error {
	return fmt.Errorf("%w (operation: %s)", errReadOnly, operation)
}

// syncLayers leaves in the table the layers declared in the embedded registry.
// It only writes what changes, so that adopting a live database does not touch
// it without reason.
func (s *Service) syncLayers(ctx context.Context) error {
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		for _, layer := range s.registry.Layers {
			_, err := tx.ExecContext(ctx, `
				INSERT INTO layers (name, description, schema_file, is_coordination,
				                    search_excluded, alias_of,
				                    added_by, deprecated, lifecycle, since_version,
				                    ingest_allowed)
				VALUES (?, ?, '', ?, ?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT(name) DO UPDATE SET
				  description = excluded.description,
				  is_coordination = excluded.is_coordination,
				  search_excluded = excluded.search_excluded,
				  alias_of = excluded.alias_of,
				  deprecated = excluded.deprecated,
				  lifecycle = excluded.lifecycle,
				  since_version = excluded.since_version,
				  ingest_allowed = excluded.ingest_allowed
				WHERE description IS NOT excluded.description
				   OR is_coordination IS NOT excluded.is_coordination
				   OR search_excluded IS NOT excluded.search_excluded
				   OR alias_of IS NOT excluded.alias_of
				   OR deprecated IS NOT excluded.deprecated
				   OR lifecycle IS NOT excluded.lifecycle
				   OR since_version IS NOT excluded.since_version
				   OR ingest_allowed IS NOT excluded.ingest_allowed`,
				layer.Name, layer.Description, layer.IsCoordination, layer.SearchExcluded,
				orNull(layer.AliasOf), layer.AddedBy,
				layer.Deprecated, layer.Lifecycle, layer.SinceVersion, layer.IngestAllowed)
			if err != nil {
				return fmt.Errorf("sync layer %q: %w", layer.Name, err)
			}
		}
		return nil
	})
}

// truncate clips a text to the requested budget while keeping the search match:
// a truncation that eats what you were looking for is not a summary, it is a
// shorter wrong answer.
func truncate(text string, budget int, term string) string {
	if budget <= 0 {
		budget = DefaultMaxChars
	}
	runes := []rune(text)
	if len(runes) <= budget {
		return text
	}
	if budget == 1 {
		return "…"
	}
	start := 0
	if pos := matchPosition(text, term); pos > 0 {
		start = max(0, pos-budget/3)
	}
	contentBudget := budget
	if start > 0 {
		contentBudget--
	}
	end := min(len(runes), start+contentBudget)
	tail := end < len(runes)
	if tail {
		contentBudget--
		end = min(len(runes), start+contentBudget)
	}
	excerpt := string(runes[start:end])
	return strings.Repeat("…", btoi(start > 0)) + excerpt + strings.Repeat("…", btoi(tail))
}

func btoi(value bool) int {
	if value {
		return 1
	}
	return 0
}

func matchPosition(text, term string) int {
	lower, positions := lowerWithPositions(text)
	for _, part := range strings.Split(term, "+") {
		if part == "" {
			continue
		}
		if i := strings.Index(lower, strings.ToLower(part)); i >= 0 {
			return positions[i]
		}
	}
	return -1
}

func lowerWithPositions(text string) (string, []int) {
	var lower strings.Builder
	positions := make([]int, 0, len(text))
	for position, char := range []rune(text) {
		folded := string(unicode.ToLower(char))
		lower.WriteString(folded)
		for range len(folded) {
			positions = append(positions, position)
		}
	}
	return lower.String(), positions
}
