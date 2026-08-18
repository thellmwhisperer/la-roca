package supportreport

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/distribution/bundledplugin"
	"github.com/thellmwhisperer/la-roca/internal/distribution/migrationledger"
	"github.com/thellmwhisperer/la-roca/internal/distribution/plugininstall"
	"github.com/thellmwhisperer/la-roca/internal/distribution/rocacorpus"
	"github.com/thellmwhisperer/la-roca/internal/distribution/rocacron"
	"github.com/thellmwhisperer/la-roca/internal/distribution/rocaops"
	"github.com/thellmwhisperer/la-roca/internal/distribution/rocavector"
	"github.com/thellmwhisperer/la-roca/internal/ingest"
	"github.com/thellmwhisperer/la-roca/internal/provider/config"
	"github.com/thellmwhisperer/la-roca/internal/provider/plugin"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
)

const (
	Kind                    = "roca-support-report"
	vectorCompletionFile    = "completion.json"
	FederationFresh         = "fresh"
	FederationLegacyOnly    = "legacy-only"
	FederationMigrating     = "migrating"
	FederationFederated     = "federated"
	FederationUninitialized = "uninitialized"
	FederationLegacyServing = "legacy-serving"
	OriginBundled           = "bundled"
	OriginExternal          = "external"
	OriginLocalDirectory    = "local-directory"
	OriginRemote            = "remote"
)

var (
	featureFlagOrder = []string{
		"strict_input", "ask_missing_referent", "plugins", "vector", "cron",
		"artifact_refresh", "roca_ops", "release_redirects",
	}
	corpusFamilies = []string{
		"sessions", "exchanges", "memories", "tool_uses", "thinking_blocks", "ingest_file_state",
	}
	archiveFamilies = []string{
		"session_versions", "exchange_versions", "tool_use_versions",
		"thinking_block_versions", "ingest_file_state_versions",
	}
	opsFamilies = []string{"memories", "memory_records"}
)

type Snapshot struct {
	Kind        string                  `json:"kind"`
	GeneratedAt string                  `json:"generated_at"`
	Identity    Identity                `json:"identity"`
	Plugins     []Plugin                `json:"plugins"`
	Features    map[string]bool         `json:"features"`
	Federation  Federation              `json:"federation"`
	Health      []service.HealthVerdict `json:"health"`
	Vector      *Vector                 `json:"vector,omitempty"`
	Ingest      Ingest                  `json:"ingest"`
}

type Identity struct {
	Version       string `json:"version"`
	Commit        string `json:"commit"`
	OS            string `json:"os"`
	Arch          string `json:"arch"`
	InstallLayout string `json:"install_layout"`
	BinaryShape   string `json:"binary_shape"`
}

type Plugin struct {
	Name            string `json:"name"`
	Version         string `json:"version"`
	Origin          string `json:"origin"`
	Source          string `json:"source"`
	Checksum        string `json:"checksum"`
	StateDirPresent bool   `json:"state_directory_present"`
}

type Federation struct {
	Mode            string      `json:"mode"`
	Serving         string      `json:"serving"`
	CorpusCustody   string      `json:"corpus_custody"`
	CutoverEligible bool        `json:"cutover_eligible"`
	Stores          []Store     `json:"stores"`
	Migrations      []Migration `json:"migrations"`
}

type Store struct {
	Name     string         `json:"name"`
	Present  bool           `json:"present"`
	Readable bool           `json:"readable"`
	Families map[string]int `json:"families,omitempty"`
}

type Migration struct {
	Plugin          string `json:"plugin"`
	Name            string `json:"name"`
	State           string `json:"state"`
	CutoverEligible bool   `json:"cutover_eligible"`
}

type Vector struct {
	Model      string         `json:"model"`
	Dimensions int            `json:"dimensions"`
	Chunks     map[string]int `json:"chunks"`
	StoreBytes int64          `json:"store_bytes"`
	LastDelta  *Delta         `json:"last_delta,omitempty"`
}

type Delta struct {
	ExitStatus int    `json:"exit_status"`
	Added      int    `json:"added"`
	Updated    int    `json:"updated"`
	Removed    int    `json:"removed"`
	Unchanged  int    `json:"unchanged"`
	Chunks     int    `json:"chunks"`
	FinishedAt string `json:"finished_at,omitempty"`
}

type Ingest struct {
	DetectedAgents []string `json:"detected_agents"`
	LastIngestAt   string   `json:"last_ingest_at,omitempty"`
}

// Options is everything the collector needs from the CLI without opening
// the write-capable service.
type Options struct {
	Version    string
	Commit     string
	Paths      config.Paths
	File       config.File
	Home       string
	PluginRoot string
	Prefix     string
	Sources    ingest.Roots
	Now        time.Time
}

func Collect(ctx context.Context, opts Options) (Snapshot, error) {
	now := opts.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	plugins := listSupportPlugins(opts.PluginRoot)
	coreStore, coreClose := openSupportStore(opts.Paths.DB)
	defer coreClose()
	corpusPath := filepath.Join(opts.PluginRoot, rocacorpus.Name, rocacorpus.DatabaseFilename)
	opsPath := filepath.Join(opts.PluginRoot, rocaops.Name, rocaops.DatabaseFilename)
	cronPath := filepath.Join(opts.PluginRoot, rocacron.Name, rocacron.DatabaseFilename)
	corpusStore, corpusClose := openSupportStore(corpusPath)
	defer corpusClose()
	opsStore, opsClose := openSupportStore(opsPath)
	defer opsClose()
	cronStore, cronClose := openSupportStore(cronPath)
	defer cronClose()
	core, corpus, ops, cron := coreStore.db, corpusStore.db, opsStore.db, cronStore.db

	coreFamilies := countFamilies(ctx, core, corpusFamilies)
	corpusFamiliesCounts := countFamilies(ctx, corpus, append(slices.Clone(corpusFamilies), archiveFamilies...))
	opsFamiliesCounts := countFamilies(ctx, ops, opsFamilies)
	stores := []Store{
		{Name: "core", Present: coreStore.present, Readable: core != nil, Families: coreFamilies},
		{Name: "plugin-corpus", Present: corpusStore.present, Readable: corpus != nil, Families: corpusFamiliesCounts},
		{Name: "plugin-ops", Present: opsStore.present, Readable: ops != nil, Families: opsFamiliesCounts},
		{Name: "plugin-cron", Present: cronStore.present, Readable: cron != nil},
	}
	migrations := collectSupportMigrations(ctx, "roca-corpus", corpus, "roca-ops", ops, "roca-cron", cron)
	serving := string(opts.File.Layout.Serving)
	if serving == "" {
		serving = string(config.LayoutLegacyServing)
	}
	mode, custody, cutoverEligible := classifyFederation(
		serving, stores, coreFamilies, corpusFamiliesCounts, migrations)
	federation := Federation{
		Mode: mode, Serving: serving, CorpusCustody: custody, CutoverEligible: cutoverEligible,
		Stores: stores, Migrations: migrations,
	}
	return Snapshot{
		Kind:        Kind,
		GeneratedAt: now.Format(time.RFC3339),
		Identity: Identity{
			Version:       opts.Version,
			Commit:        opts.Commit,
			OS:            runtime.GOOS,
			Arch:          runtime.GOARCH,
			InstallLayout: installLayout(opts.Paths),
			BinaryShape:   binaryShape(opts.Home, opts.Prefix),
		},
		Plugins:    plugins,
		Features:   featureFlags(opts.File.Features),
		Federation: federation,
		Health:     service.HealthVerdicts(ctx, []*sql.DB{ops, core}, []*sql.DB{core, corpus}),
		Vector:     collectVector(ctx, opts.PluginRoot),
		Ingest: Ingest{
			DetectedAgents: orEmptyStrings(ingest.DetectAgents(opts.Sources)),
			LastIngestAt:   lastIngestAt(ctx, corpus, core),
		},
	}, nil
}

func listSupportPlugins(root string) []Plugin {
	if root == "" {
		return nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var listed []Plugin
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		manifest, err := plugininstall.ReadManifest(filepath.Join(root, entry.Name()))
		if err != nil {
			continue
		}
		origin, source := PluginOrigin(manifest.Source)
		listed = append(listed, Plugin{
			Name:            manifest.Name,
			Version:         manifest.Version,
			Origin:          origin,
			Source:          source,
			Checksum:        manifest.Checksum,
			StateDirPresent: stateDirPresent(filepath.Join(root, entry.Name()), manifest.StateDir),
		})
	}
	slices.SortFunc(listed, func(a, b Plugin) int {
		return strings.Compare(a.Name, b.Name)
	})
	if listed == nil {
		return []Plugin{}
	}
	return listed
}

func PluginOrigin(source string) (string, string) {
	if source == plugin.BundledSource {
		return OriginBundled, source
	}
	parsed, parseErr := url.Parse(source)
	if parseErr == nil && strings.EqualFold(parsed.Scheme, "file") {
		return OriginExternal, OriginLocalDirectory
	}
	if looksLikeFilesystemPath(source) {
		return OriginExternal, OriginLocalDirectory
	}
	if parseErr == nil && parsed.Scheme != "" {
		parsed.User = nil
		parsed.RawQuery = ""
		parsed.ForceQuery = false
		parsed.Fragment = ""
		parsed.RawFragment = ""
		if parsed.Opaque != "" {
			return OriginExternal, OriginRemote
		}
		return OriginExternal, parsed.String()
	}
	if strings.Contains(source, "://") {
		return OriginExternal, OriginRemote
	}
	return OriginExternal, source
}

func looksLikeFilesystemPath(source string) bool {
	if source == "" {
		return false
	}
	if filepath.IsAbs(source) || strings.HasPrefix(source, "~") || strings.HasPrefix(source, ".") {
		return true
	}
	return strings.ContainsAny(source, `/\`) && !strings.Contains(source, "://") &&
		!pluginRepoReference(source)
}

func pluginRepoReference(source string) bool {
	return strings.Count(source, "/") == 1 && !strings.ContainsAny(source, `\:`)
}

func stateDirPresent(directory, name string) bool {
	if name == "" {
		return false
	}
	info, err := os.Stat(filepath.Join(directory, name))
	return err == nil && info.IsDir()
}

func featureFlags(features config.FeaturesConfig) map[string]bool {
	return map[string]bool{
		"strict_input":         features.StrictInput,
		"ask_missing_referent": features.AskMissingReferent,
		"plugins":              features.Plugins,
		"vector":               features.Vector,
		"cron":                 features.Cron,
		"artifact_refresh":     features.ArtifactRefresh,
		"roca_ops":             features.RocaOps,
		"release_redirects":    features.ReleaseRedirects,
	}
}

func installLayout(paths config.Paths) string {
	if paths.Home != "" && paths.DB == filepath.Join(paths.Home, config.DirOwn, config.FileDB) {
		return "default-home"
	}
	return "custom-data-dir"
}

func binaryShape(home, prefix string) string {
	executable, err := os.Executable()
	if err != nil {
		return "unknown"
	}
	directory := filepath.Dir(executable)
	if prefix != "" && sameFilepath(directory, prefix) {
		return "prefix"
	}
	if home != "" && sameFilepath(directory, filepath.Join(home, ".local", "bin")) {
		return "home-local"
	}
	return "other"
}

func sameFilepath(a, b string) bool {
	left, err := filepath.Abs(a)
	if err != nil {
		return false
	}
	right, err := filepath.Abs(b)
	return err == nil && left == right
}

type supportStore struct {
	present bool
	db      *sql.DB
}

func openSupportStore(path string) (supportStore, func()) {
	if path == "" {
		return supportStore{}, func() {}
	}
	if _, err := os.Stat(path); err != nil {
		return supportStore{}, func() {}
	}
	store := supportStore{present: true}
	db, err := bundledplugin.OpenDatabase(path, true)
	if err != nil {
		return store, func() {}
	}
	var schemaVersion int
	if err := db.QueryRow("PRAGMA schema_version").Scan(&schemaVersion); err != nil {
		db.Close()
		return store, func() {}
	}
	store.db = db
	return store, func() { _ = db.Close() }
}

func openSupportDB(path string) (*sql.DB, func()) {
	store, closeStore := openSupportStore(path)
	return store.db, closeStore
}

func countFamilies(ctx context.Context, db *sql.DB, families []string) map[string]int {
	if db == nil {
		return nil
	}
	counts := make(map[string]int, len(families))
	for _, family := range families {
		if !supportTablePresent(ctx, db, family) {
			continue
		}
		var count int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+family).Scan(&count); err != nil {
			continue
		}
		counts[family] = count
	}
	if len(counts) == 0 {
		return nil
	}
	return counts
}

func supportTablePresent(ctx context.Context, db *sql.DB, name string) bool {
	var exists int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master
		WHERE type = 'table' AND name = ?`, name).Scan(&exists); err != nil {
		return false
	}
	return exists != 0
}

func collectSupportMigrations(ctx context.Context, pairs ...any) []Migration {
	var listed []Migration
	for i := 0; i+1 < len(pairs); i += 2 {
		name, _ := pairs[i].(string)
		db, _ := pairs[i+1].(*sql.DB)
		if db == nil {
			continue
		}
		migrations, err := migrationledger.ListMigrations(ctx, db)
		if err != nil {
			continue
		}
		for _, migration := range migrations {
			listed = append(listed, Migration{
				Plugin: name, Name: migration.Name, State: string(migration.State),
				CutoverEligible: (migration.State == migrationledger.StateVerified ||
					migration.State == migrationledger.StateVerifiedEmpty) &&
					migration.VerificationDigest != "",
			})
		}
	}
	slices.SortFunc(listed, func(a, b Migration) int {
		if compared := strings.Compare(a.Plugin, b.Plugin); compared != 0 {
			return compared
		}
		return strings.Compare(a.Name, b.Name)
	})
	if listed == nil {
		return []Migration{}
	}
	return listed
}

func corpusCustody(stores []Store, core, corpus map[string]int, cutoverEligible bool) string {
	coreStore, corpusStore := storeNamed(stores, "core"), storeNamed(stores, "plugin-corpus")
	if coreStore.Present && !coreStore.Readable || corpusStore.Present && !corpusStore.Readable {
		return "unknown"
	}
	coreText := core["sessions"] + core["exchanges"] + core["thinking_blocks"]
	pluginText := corpus["sessions"] + corpus["exchanges"] + corpus["thinking_blocks"] +
		corpus["session_versions"] + corpus["exchange_versions"] + corpus["thinking_block_versions"]
	if cutoverEligible {
		if pluginText > 0 {
			return "plugin-corpus"
		}
		return "empty"
	}
	switch {
	case coreText == 0 && pluginText == 0:
		return "empty"
	case pluginText > 0 && coreText == 0:
		return "plugin-corpus"
	case coreText > 0 && pluginText == 0:
		return "legacy-core"
	default:
		return "split"
	}
}

func classifyFederation(serving string, stores []Store, core, corpus map[string]int,
	migrations []Migration) (string, string, bool) {
	coreStore := storeNamed(stores, "core")
	corpusStore := storeNamed(stores, "plugin-corpus")
	opsStore := storeNamed(stores, "plugin-ops")
	cronStore := storeNamed(stores, "plugin-cron")
	pluginsPresent := corpusStore.Present || opsStore.Present || cronStore.Present
	cutoverEligible := supportCutoverEligible(stores, migrations)
	activeFederation := serving == string(config.LayoutCutover) && cutoverEligible
	custody := corpusCustody(stores, core, corpus, activeFederation)
	if !pluginsPresent {
		if coreStore.Present {
			return FederationLegacyOnly, custody, false
		}
		return FederationUninitialized, custody, false
	}
	if serving == string(config.LayoutCutover) && cutoverEligible {
		return FederationFederated, custody, true
	}
	if serving == string(config.LayoutCutover) || serving == string(config.LayoutShadowEqual) ||
		migrationInFlight(migrations) || len(migrations) > 0 && !cutoverEligible {
		return FederationMigrating, custody, cutoverEligible
	}
	if custody == "empty" {
		return FederationFresh, custody, cutoverEligible
	}
	return FederationLegacyServing, custody, cutoverEligible
}

func supportCutoverEligible(stores []Store, migrations []Migration) bool {
	for _, name := range []string{"core", "plugin-corpus", "plugin-ops"} {
		store := storeNamed(stores, name)
		if !store.Present || !store.Readable {
			return false
		}
	}
	required := map[string]map[string]bool{
		"roca-ops": {"data2-memory-custody": false},
		"roca-corpus": {
			"corpus-archive-sessions":          false,
			"corpus-archive-exchanges":         false,
			"corpus-archive-tool-uses":         false,
			"corpus-archive-thinking-blocks":   false,
			"corpus-archive-ingest-file-state": false,
			"corpus-archive-reconciliation-v1": false,
		},
	}
	for _, migration := range migrations {
		if names := required[migration.Plugin]; names != nil {
			if _, ok := names[migration.Name]; ok && migration.CutoverEligible {
				names[migration.Name] = true
			}
		}
	}
	for _, names := range required {
		for _, eligible := range names {
			if !eligible {
				return false
			}
		}
	}
	return true
}

func storeNamed(stores []Store, name string) Store {
	for _, store := range stores {
		if store.Name == name {
			return store
		}
	}
	return Store{Name: name}
}

func migrationInFlight(migrations []Migration) bool {
	for _, migration := range migrations {
		if migration.State == string(migrationledger.StatePrepared) ||
			migration.State == string(migrationledger.StateBatchInProgress) {
			return true
		}
	}
	return false
}

func collectVector(ctx context.Context, pluginRoot string) *Vector {
	if pluginRoot == "" {
		return nil
	}
	state := filepath.Join(pluginRoot, rocavector.Name, rocavector.StateDir)
	path := filepath.Join(state, "vector.db")
	info, err := os.Stat(path)
	if err != nil {
		return nil
	}
	report := &Vector{StoreBytes: info.Size(), Chunks: map[string]int{}}
	db, closer := openSupportDB(path)
	defer closer()
	if db != nil {
		_ = db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='model'`).Scan(&report.Model)
		var dimensionText string
		if err := db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='dimensions'`).
			Scan(&dimensionText); err == nil {
			report.Dimensions, _ = strconv.Atoi(dimensionText)
		}
		if rows, err := db.QueryContext(ctx,
			`SELECT source_kind, COUNT(*) FROM chunks GROUP BY source_kind ORDER BY source_kind`); err == nil {
			defer rows.Close()
			for rows.Next() {
				var kind string
				var count int
				if rows.Scan(&kind, &count) == nil {
					report.Chunks[kind] = count
				}
			}
		}
	}
	report.LastDelta = readVectorDelta(filepath.Join(state, vectorCompletionFile))
	return report
}

func readVectorDelta(path string) *Delta {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var completion struct {
		ExitStatus int `json:"exit_status"`
		Delta      struct {
			Added     int `json:"added"`
			Updated   int `json:"updated"`
			Removed   int `json:"removed"`
			Unchanged int `json:"unchanged"`
			Chunks    int `json:"chunks"`
		} `json:"counts"`
		FinishedAt time.Time `json:"finished_at"`
	}
	if json.Unmarshal(raw, &completion) != nil {
		return nil
	}
	delta := &Delta{
		ExitStatus: completion.ExitStatus,
		Added:      completion.Delta.Added,
		Updated:    completion.Delta.Updated,
		Removed:    completion.Delta.Removed,
		Unchanged:  completion.Delta.Unchanged,
		Chunks:     completion.Delta.Chunks,
	}
	if !completion.FinishedAt.IsZero() {
		delta.FinishedAt = completion.FinishedAt.UTC().Format(time.RFC3339)
	}
	return delta
}

func lastIngestAt(ctx context.Context, dbs ...*sql.DB) string {
	var latest string
	for _, db := range dbs {
		if db == nil || !supportTablePresent(ctx, db, "ingest_file_state") {
			continue
		}
		var value sql.NullString
		if err := db.QueryRowContext(ctx, `SELECT MAX(last_synced_at) FROM ingest_file_state`).
			Scan(&value); err != nil || !value.Valid || value.String == "" {
			continue
		}
		if value.String > latest {
			latest = value.String
		}
	}
	return latest
}

func Render(snapshot Snapshot) string {
	var b strings.Builder
	b.WriteString("```text\n")
	fmt.Fprintf(&b, "roca support report  %s\n", snapshot.GeneratedAt)
	b.WriteString("\nIDENTITY\n")
	fmt.Fprintf(&b, "version: %s\n", snapshot.Identity.Version)
	fmt.Fprintf(&b, "commit: %s\n", snapshot.Identity.Commit)
	fmt.Fprintf(&b, "os/arch: %s/%s\n", snapshot.Identity.OS, snapshot.Identity.Arch)
	fmt.Fprintf(&b, "install_layout: %s\n", snapshot.Identity.InstallLayout)
	fmt.Fprintf(&b, "binary_shape: %s\n", snapshot.Identity.BinaryShape)

	b.WriteString("\nPLUGINS\n")
	if len(snapshot.Plugins) == 0 {
		b.WriteString("none\n")
	}
	for _, item := range snapshot.Plugins {
		fmt.Fprintf(&b, "%s %s origin=%s source=%s checksum=%s state_dir=%t\n",
			item.Name, item.Version, item.Origin, item.Source, item.Checksum, item.StateDirPresent)
	}

	b.WriteString("\nFEATURE FLAGS\n")
	for _, name := range featureFlagOrder {
		fmt.Fprintf(&b, "%s: %s\n", name, onOff(snapshot.Features[name]))
	}

	b.WriteString("\nFEDERATION\n")
	fmt.Fprintf(&b, "mode: %s\n", snapshot.Federation.Mode)
	fmt.Fprintf(&b, "serving: %s\n", snapshot.Federation.Serving)
	fmt.Fprintf(&b, "corpus_custody: %s\n", snapshot.Federation.CorpusCustody)
	fmt.Fprintf(&b, "cutover_eligible: %t\n", snapshot.Federation.CutoverEligible)
	b.WriteString("stores:\n")
	for _, store := range snapshot.Federation.Stores {
		fmt.Fprintf(&b, "  %s: %s", store.Name, storeStatus(store))
		if len(store.Families) > 0 {
			b.WriteString("  ")
			b.WriteString(renderFamilyCounts(store.Families))
		}
		b.WriteByte('\n')
	}
	if len(snapshot.Federation.Migrations) == 0 {
		b.WriteString("migrations: none\n")
	} else {
		b.WriteString("migrations:\n")
		for _, migration := range snapshot.Federation.Migrations {
			fmt.Fprintf(&b, "  %s %s: %s eligible=%t\n", migration.Plugin, migration.Name,
				migration.State, migration.CutoverEligible)
		}
	}

	b.WriteString("\nHEALTH\n")
	pass, warn, fail, skipped := 0, 0, 0, 0
	for _, check := range snapshot.Health {
		switch check.Status {
		case service.HealthPass:
			pass++
		case service.HealthWarn:
			warn++
		case service.HealthFail:
			fail++
		default:
			skipped++
		}
		fmt.Fprintf(&b, "%s: %s\n", check.Name, check.Status)
	}
	fmt.Fprintf(&b, "summary: pass=%d warn=%d fail=%d skipped=%d\n", pass, warn, fail, skipped)

	b.WriteString("\nVECTOR\n")
	if snapshot.Vector == nil {
		b.WriteString("absent\n")
	} else {
		fmt.Fprintf(&b, "model: %s\n", snapshot.Vector.Model)
		fmt.Fprintf(&b, "dimensions: %d\n", snapshot.Vector.Dimensions)
		fmt.Fprintf(&b, "store_bytes: %d\n", snapshot.Vector.StoreBytes)
		fmt.Fprintf(&b, "chunks: %s\n", renderFamilyCounts(snapshot.Vector.Chunks))
		if snapshot.Vector.LastDelta != nil {
			delta := snapshot.Vector.LastDelta
			fmt.Fprintf(&b, "last_delta: exit=%d added=%d updated=%d removed=%d unchanged=%d chunks=%d",
				delta.ExitStatus, delta.Added, delta.Updated, delta.Removed, delta.Unchanged, delta.Chunks)
			if delta.FinishedAt != "" {
				fmt.Fprintf(&b, " finished=%s", delta.FinishedAt)
			}
			b.WriteByte('\n')
		}
	}

	b.WriteString("\nINGEST\n")
	if len(snapshot.Ingest.DetectedAgents) == 0 {
		b.WriteString("detected_agents: none\n")
	} else {
		fmt.Fprintf(&b, "detected_agents: %s\n", strings.Join(snapshot.Ingest.DetectedAgents, ","))
	}
	if snapshot.Ingest.LastIngestAt == "" {
		b.WriteString("last_ingest_at: none\n")
	} else {
		fmt.Fprintf(&b, "last_ingest_at: %s\n", snapshot.Ingest.LastIngestAt)
	}
	b.WriteString("```")
	return b.String()
}

func onOff(on bool) string {
	if on {
		return "on"
	}
	return "off"
}

func storeStatus(store Store) string {
	if store.Readable {
		return "present"
	}
	if store.Present {
		return "unreadable"
	}
	return "absent"
}

func orEmptyStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func renderFamilyCounts(counts map[string]int) string {
	names := make([]string, 0, len(counts))
	for name := range counts {
		names = append(names, name)
	}
	slices.Sort(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, fmt.Sprintf("%s=%d", name, counts[name]))
	}
	return strings.Join(parts, " ")
}
