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

	"github.com/thellmwhisperer/la-roca/internal/artifact"
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
	// QueryTimeout bounds execution after SQL passes the read-only gate.
	// QueryTimeoutSet distinguishes an explicit zero, which disables the bound,
	// from an absent setting, which uses DefaultQueryTimeout.
	QueryTimeout    time.Duration
	QueryTimeoutSet bool
	// DisableStrictInput is the opt-out escape hatch for the experimental
	// prompt-attack signature gate. Its zero value keeps the gate enabled.
	DisableStrictInput bool
	// DisableMissingReferentAsk is the opt-out escape hatch for asking the
	// operator to name a referent the question left generic instead of letting
	// the model guess one. Its zero value keeps the ask enabled, and it is its
	// own switch: an installation that wants the guess back does not thereby
	// lose the signature gate it never asked to turn off.
	DisableMissingReferentAsk bool
	// PluginDir is the installation's ~/.roca/plugins directory. Empty disables
	// plugin discovery, which keeps embedded and explicitly selected databases
	// independent of the process HOME.
	PluginDir string
	// PluginsEnabled is the experimental features.plugins gate. False makes the
	// entire plugin query path inert even when PluginDir exists.
	PluginsEnabled bool
	// ReadOnly refuses in the service, before any database I/O.
	ReadOnly bool
	// Providers is the resolved model cascade. Its zero value is a service that
	// contacts no provider, which is what an installation with no model
	// configured needs.
	Providers provider.Cascade
	// Interpreters is the cascade the result rows travel to, when the operator
	// declared one. Its zero value is the installation that does not split the
	// two inferences: whoever wrote the SQL also reads the rows. Declared, it is
	// what keeps the rows on this machine while the question goes to a frontier
	// model.
	Interpreters provider.Cascade
	// Explorers is the optional stronger order for deep investigation prose.
	// When it cannot serve, deep exploration falls through to Interpreters and
	// then Providers; plain exploration starts at Interpreters.
	Explorers provider.Cascade
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
	schemaMu sync.Mutex
	schemaOK bool

	gateOnce    sync.Once
	gate        *sqlgate.Gate
	gateFailure error
}

// Open opens the database. Its schema is adopted before the first data operation.
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

func (s *Service) ensureSchema(ctx context.Context) (search.Report, error) {
	s.schemaMu.Lock()
	defer s.schemaMu.Unlock()
	if s.schemaOK {
		return search.Report{}, nil
	}
	var index search.Report
	if s.opts.ReadOnly {
		report, err := store.Inspect(ctx, s.db)
		if err != nil {
			return index, err
		}
		if report.Verdict != store.VerdictCurrent {
			return index, fmt.Errorf("the database schema requires adoption, but La Roca is in read-only mode: %s", report.Reason)
		}
	} else {
		if _, err := store.Adopt(ctx, s.db, s.opts.BackupDir); err != nil {
			return index, err
		}
		started := time.Now()
		var err error
		index.LexicalBuilt, err = search.EnsureTokenizer(ctx, s.db, s.opts.Progress)
		index.ElapsedMS = time.Since(started).Milliseconds()
		if err != nil {
			return index, err
		}
	}
	s.schemaOK = true
	return index, nil
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
	Bedrock    *Bedrock       `json:"bedrock"`
	Search     *search.Report `json:"search_index,omitempty"`
	// Model and Ingest are the rest of the bootstrap: whether a model is going
	// to answer, and what the first read of the disk found. Neither can fail
	// the command, and both report.
	Model                  *InitModel    `json:"model"`
	Ingest                 *IngestResult `json:"ingest"`
	PromptPath             string        `json:"prompt_path"`
	Prompt                 string        `json:"-"`
	Warnings               []string      `json:"warnings,omitempty"`
	DetectedModelBinaries  []string      `json:"detected_model_binaries"`
	MissingModelBinaries   []string      `json:"missing_model_binaries"`
	FactoryDefault         bool          `json:"factory_default"`
	FactoryDefaultProvider string        `json:"factory_default_provider,omitempty"`
	RowsBefore             ingest.Tables `json:"-"`
	SetupElapsedMS         int64         `json:"-"`
	ModelElapsedMS         int64         `json:"-"`
	TotalElapsedMS         int64         `json:"-"`
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
	// CommandTransport means the chosen model runs through an agent CLI.
	CommandTransport bool `json:"command_transport,omitempty"`
}

// presentationPromptSignature opens every prompt.md this product has written.
const presentationPromptSignature = "## La Roca — local semantic memory\n"

const presentationPrompt = presentationPromptSignature +
	"La Roca contains local session history, curated memories, handoffs, decisions, " +
	"and tool traces from your agents.\n" +
	"when to query: at session start, before repeating research, and whenever prior " +
	"context or a decision may exist.\n" +
	"With a shell, use `roca query \"<natural question>\"`; preserve durable context " +
	"with `roca store --agent <harness> --model <model>` so CLI authorship is explicit.\n" +
	"Data = `roca query`; human reading = `roca query --full`; raw SQL = `roca exec`.\n" +
	"Investigations start with `roca explore --deep \"<one bare word>\"`, then plain `roca explore` radius probes.\n" +
	"Without a shell, use the MCP equivalents: `roca_query`, `roca_explore`, and `roca_store`.\n" +
	"Authorship is automatic over MCP; CLI detection is conservative, so pass --agent and --model.\n" +
	"On first bootstrap, `roca init` asks new or adopt, then chooses the answering model before its harness.\n" +
	"La Roca never edits agent instruction files; a human chooses where to paste this block.\n"

// PresentationPrompt is the product-owned part of prompt.md. Distribution
// lifecycle code uses the same bytes service.Init installs.
func PresentationPrompt() string { return presentationPrompt }

// PresentationPromptSignature is how a prompt.md an earlier release generated
// is recognized as this product's own text rather than the operator's, so a
// migration replaces it and a purge still owns it.
func PresentationPromptSignature() string { return presentationPromptSignature }

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
	s.schemaMu.Lock()
	s.schemaOK = true
	s.schemaMu.Unlock()
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
		DBPath:                s.db.Path(),
		ConfigPath:            s.opts.ConfigPath,
		Database:              state,
		Verdict:               string(adoption.Verdict),
		Structures:            adoption.RequiredStructures,
		Orphans:               adoption.Orphans,
		Repairs:               adoption.Repairs,
		BackupPath:            adoption.BackupPath,
		Layers:                len(s.registry.Layers),
		Bytes:                 bytes,
		Rows:                  rows,
		RowsBefore:            rows,
		DetectedModelBinaries: append([]string(nil), s.opts.Providers.DetectedBinaries...),
		MissingModelBinaries:  provider.MissingCommandPresets(s.opts.Providers.DetectedBinaries),
		FactoryDefault:        s.opts.Providers.FactoryDefault,
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
	if result.FactoryDefault && result.Model.Ready {
		result.FactoryDefaultProvider = result.Model.Provider
	}
	result.ModelElapsedMS = time.Since(modelStarted).Milliseconds()
	if result.Model.Ready {
		progress("model: " + result.Model.Provider + "/" + result.Model.Model + " will answer")
	} else {
		progress("model: no provider will answer · " + result.Model.Reason)
	}
	result.Rows = result.Ingest.After
	result.Bedrock, err = s.bedrock(ctx)
	if err != nil {
		return InitResult{}, err
	}
	if info, statErr := os.Stat(s.db.Path()); statErr == nil {
		result.Bytes = info.Size()
	}
	result.PromptPath = filepath.Join(s.dataDir(), "prompt.md")
	var promptBackup string
	result.Prompt, promptBackup, err = installPresentationPrompt(result.PromptPath)
	// The recovery copy is named before the error is, and whether or not there
	// was one: a migration that moved the operator's own bytes out of prompt.md
	// is the only place those bytes are left, and init is where they are told.
	if promptBackup != "" {
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("the previous agent prompt was replaced; its content is kept at %s", promptBackup))
	}
	if err != nil {
		failedPath := result.PromptPath
		result.PromptPath = ""
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("write the agent prompt at %s: %v", failedPath, err))
	}
	result.TotalElapsedMS = time.Since(started).Milliseconds()
	return result, nil
}

// installPresentationPrompt returns the prompt on disk and, when the one-time
// migration made one, the recovery copy holding what was there before.
func installPresentationPrompt(path string) (string, string, error) {
	if body, err := os.ReadFile(path); err == nil {
		if _, parseErr := artifact.Parse(string(body)); parseErr == nil {
			return string(body), "", nil
		}
	} else if !os.IsNotExist(err) {
		return "", "", err
	}
	out, err := artifact.RefreshFile(artifact.FileRequest{
		Path: path, System: presentationPrompt,
		LegacySignature: presentationPromptSignature, Enabled: true,
	})
	if err != nil {
		return "", out.Backup, err
	}
	body, err := os.ReadFile(path)
	return string(body), out.Backup, err
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
	for i, attempt := range cascade.Diagnose(ctx) {
		if attempt.Ready {
			transport, command := cascade.Providers[i].(interface{ CommandTransport() bool })
			return &InitModel{Ready: true, Provider: attempt.Name, Model: attempt.ModelID,
				CommandTransport: command && transport.CommandTransport()}
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

// truncate clips a text to the requested budget while keeping both the leading
// subject and the search match.
func truncate(text string, budget int, term string) string {
	pos, matchEnd := matchSpan(text, term)
	return Excerpt(text, budget, pos, matchEnd)
}

// Excerpt clips a text to the requested budget while keeping both the leading
// subject and the match running from matchStart to matchEnd in runes, with a
// negative matchStart for a text that carries no match. When those segments do
// not fit together, the ellipsis sits between them instead of silently
// replacing the subject, so no row can be read as being about whoever the match
// names. This is the single owner of that policy: the stored envelope and the
// rendered preview clip the same way.
func Excerpt(text string, budget, matchStart, matchEnd int) string {
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
	if budget < 4 {
		return string(runes[:budget-1]) + "…"
	}
	if matchStart < 0 || matchEnd <= budget-1 {
		return string(runes[:budget-1]) + "…"
	}

	head := max(1, (budget-2)/2)
	suffixBudget := budget - head - 1
	start := matchStart
	if start+suffixBudget >= len(runes) {
		start = max(head, len(runes)-suffixBudget)
	}
	if start <= head {
		// The head already reaches the match, so there is no gap to elide: a
		// second window would reprint what the subject just showed.
		return string(runes[:budget-1]) + "…"
	}
	tail := start+suffixBudget < len(runes)
	if tail {
		suffixBudget--
	}
	return string(runes[:head]) + "…" + string(runes[start:start+suffixBudget]) +
		strings.Repeat("…", btoi(tail))
}

func btoi(value bool) int {
	if value {
		return 1
	}
	return 0
}

func matchPosition(text, term string) int {
	position, _ := matchSpan(text, term)
	return position
}

func matchSpan(text, term string) (int, int) {
	lower, positions := lowerWithPositions(text)
	for _, part := range strings.Split(term, "+") {
		if part == "" {
			continue
		}
		folded := strings.ToLower(part)
		if i := strings.Index(lower, folded); i >= 0 {
			last := i + len(folded) - 1
			return positions[i], positions[last] + 1
		}
	}
	return -1, -1
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
