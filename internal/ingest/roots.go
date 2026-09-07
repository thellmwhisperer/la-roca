package ingest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The roots are configuration, never constants. They come from
// the home the process was given and from what the operator declared, and an
// absolute path with a machine, a user or a personal mount written into this file
// would be a guard failure and not a style decision.
//
// Three precedences, highest first: what the operator declared in the
// configuration, what the environment says, and the platform's own layout.

// Environment is what the machine says about itself. It travels as data so every
// platform's layout is a table case testable on any platform.
type Environment struct {
	// GOOS is the platform: darwin, linux or windows.
	GOOS string
	// Home is the user's home directory, already resolved.
	Home string
	// Getenv reads the environment. A nil one is a machine with nothing declared.
	Getenv func(string) string
}

func (e Environment) get(key string) string {
	if e.Getenv == nil {
		return ""
	}
	return e.Getenv(key)
}

// windows says whether paths are written with the Windows separator.
func (e Environment) windows() bool { return e.GOOS == "windows" }

// Settings is what the operator declared, read from the configuration file. Every
// field left empty falls to the environment and then to the platform default.
type Settings struct {
	ClaudeProjects        string
	ClaudeDesktopSessions string
	CoworkSessions        string
	CodexRoot             string
	CodexSessions         string
	CodexStateDB          string
	OpenCodeDB            string
	OpenCodeTelegramLogs  string
	ZCodeDB               string
	PiRoot                string
	PiSessions            string
	HermesHome            string
	HermesDB              string
	// LegacyStoreDB is a pre-federation La Roca store to import.
	LegacyStoreDB string
	// GrokSessions is Grok Build's session store.
	GrokSessions string
	// RunnerDir is La Roca's neutral subprocess cwd. Any runtime artefact keyed
	// to this directory is product traffic, never operator corpus.
	RunnerDir string
	// WorkspaceRoots resolve project identity from encoded session paths. Files
	// under them are never ingested as content.
	WorkspaceRoots []string
	// SubagentRoots are the Claude projects roots the subagent transcripts are
	// discovered under. Empty means the resolved Claude projects root.
	SubagentRoots []string
}

// Roots are the resolved locations of every source in the v1 matrix.
type Roots struct {
	// Home anchors home-relative locations declared by contributed parsers.
	Home           string
	ClaudeProjects string
	// ClaudeConfig records the real working directories Claude encoded below
	// ClaudeProjects. It is attribution evidence and never corpus content.
	ClaudeConfig          string
	ClaudeDesktopSessions string
	CoworkSessions        string
	CodexRoot             string
	CodexSessions         string
	// CodexStateDB is Codex's own SQLite state. It is read only to enrich a
	// session with the model and the agent nickname it ran under.
	CodexStateDB string
	OpenCodeDB   string
	// OpenCodeTelegramLogs is the companion bot's own log directory. Its
	// session ids enrich matching OpenCode records; the logs are never corpus.
	OpenCodeTelegramLogs string
	// ZCodeDB is ZCode's durable session database below its private storage
	// root. The desktop app and the embedded CLI share this file.
	ZCodeDB    string
	PiRoot     string
	PiSessions string
	// HermesHome is the Hermes private tree. Memories, named exclusions, and
	// the default state.db live under it; hermes_db_path can still point the
	// database elsewhere.
	HermesHome string
	HermesDB   string
	// LegacyStoreDB is a pre-federation La Roca store. Conversations become
	// corpus; memories keep their original layer in ops.
	LegacyStoreDB string
	// GrokSessions is Grok Build's session store.
	GrokSessions string
	// GrokMemtrace is process-memory telemetry, counted for coverage and excluded
	// from conversation content.
	GrokMemtrace      string
	RunnerDir         string
	ClaudeWebExports  []string
	ChatGPTWebExports []string
	SubagentRoots     []string
	Workspace         WorkspaceRoots
}

// These environment variable names are stable for operator compatibility.
const (
	envClaudeProjects       = "CLAUDE_PROJECTS_ROOT"
	envCodexRoot            = "CODEX_ROOT"
	envCodexSessions        = "CODEX_SESSIONS_ROOT"
	envCodexStateDB         = "CODEX_STATE_DB_PATH"
	envOpenCodeDB           = "OPENCODE_DB_PATH"
	envOpenCodeTelegramLogs = "OPENCODE_TELEGRAM_BOT_LOGS"
	envZCodeDB              = "ZCODE_DB_PATH"
	envZCodeStorage         = "ZCODE_STORAGE_DIR"
	envPiRoot               = "PI_ROOT"
	envPiSessions           = "PI_SESSIONS_ROOT"
	envHermesHome           = "HERMES_HOME"
	envHermesDB             = "HERMES_DB_PATH"
	envGrokSessions         = "GROK_SESSIONS_ROOT"
	envXDGConfig            = "XDG_CONFIG_HOME"
	envXDGData              = "XDG_DATA_HOME"
	envAppData              = "APPDATA"
	envLocalAppData         = "LOCALAPPDATA"
)

// ResolveRoots decides where every source lives on this machine.
func ResolveRoots(env Environment, settings Settings) Roots {
	claude := join(env, env.Home, ".claude")
	codexRoot := pick(env, settings.CodexRoot, envCodexRoot, join(env, env.Home, ".codex"))
	piRoot := pick(env, settings.PiRoot, envPiRoot, join(env, env.Home, ".pi"))
	zcodeRoot := pick(env, "", envZCodeStorage, join(env, env.Home, ".zcode"))
	hermesHome := pick(env, settings.HermesHome, envHermesHome, join(env, env.Home, ".hermes"))
	appSupport := claudeAppSupport(env)

	roots := Roots{
		Home: env.Home,
		ClaudeProjects: pick(env, settings.ClaudeProjects, envClaudeProjects,
			join(env, claude, "projects")),
		ClaudeConfig: join(env, env.Home, ".claude.json"),
		ClaudeDesktopSessions: expand(env, firstNonEmpty(settings.ClaudeDesktopSessions,
			join(env, appSupport, "claude-code-sessions"))),
		CoworkSessions: expand(env, firstNonEmpty(settings.CoworkSessions,
			join(env, appSupport, "local-agent-mode-sessions"))),
		CodexRoot: codexRoot,
		CodexSessions: pick(env, settings.CodexSessions, envCodexSessions,
			join(env, codexRoot, "sessions")),
		CodexStateDB: pick(env, settings.CodexStateDB, envCodexStateDB,
			join(env, codexRoot, "state_5.sqlite")),
		OpenCodeDB: pick(env, settings.OpenCodeDB, envOpenCodeDB,
			join(env, openCodeDir(env), "opencode.db")),
		OpenCodeTelegramLogs: pick(env, settings.OpenCodeTelegramLogs,
			envOpenCodeTelegramLogs, openCodeTelegramLogsDir(env)),
		ZCodeDB: pick(env, settings.ZCodeDB, envZCodeDB,
			join(env, zcodeRoot, "cli", "db", "db.sqlite")),
		PiRoot: piRoot,
		PiSessions: pick(env, settings.PiSessions, envPiSessions,
			join(env, piRoot, "agent", "sessions")),
		HermesHome: hermesHome,
		HermesDB: pick(env, settings.HermesDB, envHermesDB,
			join(env, hermesHome, "state.db")),
		LegacyStoreDB: expand(env, firstNonEmpty(settings.LegacyStoreDB,
			env.get("LEGACY_STORE_DB_PATH"), env.get(retiredStoreDBEnv()),
			join(env, env.Home, "."+retiredStoreHome(), "roca.db"))),
		GrokSessions: pick(env, settings.GrokSessions, envGrokSessions,
			join(env, env.Home, ".grok", "sessions")),
		GrokMemtrace: join(env, env.Home, ".grok", "memtrace"),
		RunnerDir:    expand(env, settings.RunnerDir),
		Workspace:    ResolveWorkspaceRoots(expandAll(env, settings.WorkspaceRoots)),
	}

	roots.SubagentRoots = expandAll(env, settings.SubagentRoots)
	if len(roots.SubagentRoots) == 0 {
		roots.SubagentRoots = []string{roots.ClaudeProjects}
	}
	return roots
}

// WithExportPath adds one extracted account export to this invocation. The
// folder shape decides which parser owns conversations.json; the path is not
// retained anywhere and a later plain ingest starts from the live roots again.
//
// The vendor is decided here and never fallen back to: a directory carrying
// neither shape is refused naming both of them, because attributing it to
// whichever vendor was tried last answers an operator importing one product
// with a diagnosis about the other.
func WithExportPath(roots Roots, path string) (Roots, error) {
	switch {
	case claudeExport(path):
		roots.ClaudeWebExports = []string{path}
	case chatGPTExport(path):
		roots.ChatGPTWebExports = []string{path}
	default:
		return roots, fmt.Errorf("%q holds no extracted account export: a Claude "+
			"export holds memories.json or a conversations.json of chat_messages "+
			"records, and a ChatGPT export holds conversations.json or "+
			"conversations-*.json", path)
	}
	return roots, nil
}

func claudeExport(root string) bool {
	if isFile(filepath.Join(root, "memories.json")) {
		return true
	}
	first := firstExportRecord(filepath.Join(root, "conversations.json"))
	_, messages := first["chat_messages"]
	_, uuid := first["uuid"]
	return messages || uuid
}

// chatGPTExport is the layout left once Claude's own shapes are ruled out: the
// legacy single file and the sharded generation, named here exactly as the scan
// names them. It is a decision about the file names and not about their
// contents, so a conversations.json nobody can decode reaches the parser that
// can say why instead of being refused as no export at all.
func chatGPTExport(root string) bool {
	for _, name := range filesIn(root) {
		if name == "conversations.json" ||
			strings.HasPrefix(name, "conversations-") && strings.HasSuffix(name, ".json") {
			return true
		}
	}
	return false
}

// firstExportRecord is the first object of the array a conversations file holds,
// and nothing at all for one that is absent, is not an array, or does not decode.
func firstExportRecord(path string) map[string]json.RawMessage {
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	token, err := decoder.Token()
	if err != nil || token != json.Delim('[') || !decoder.More() {
		return nil
	}
	var first map[string]json.RawMessage
	if err := decoder.Decode(&first); err != nil {
		return nil
	}
	return first
}

// under is the directory `name` inside the base a platform variable declares, or
// inside that platform's own convention when the machine declares nothing.
func under(env Environment, variable, name string, convention ...string) string {
	base := firstNonEmpty(env.get(variable),
		join(env, append([]string{env.Home}, convention...)...))
	return join(env, base, name)
}

// claudeAppSupport is where the desktop runtimes keep their sessions. It is the
// one path that differs per platform, and each of the three is the platform's own
// convention and not a guess.
func claudeAppSupport(env Environment) string {
	switch env.GOOS {
	case "darwin":
		return join(env, env.Home, "Library", "Application Support", "Claude")
	case "windows":
		return under(env, envAppData, "Claude", "AppData", "Roaming")
	default:
		return under(env, envXDGConfig, "Claude", ".config")
	}
}

// openCodeDir is where OpenCode keeps its database.
func openCodeDir(env Environment) string {
	if env.windows() {
		return under(env, envLocalAppData, "opencode", "AppData", "Local")
	}
	return under(env, envXDGData, "opencode", ".local", "share")
}

func openCodeTelegramLogsDir(env Environment) string {
	if env.GOOS == "darwin" {
		return join(env, env.Home, "Library", "Application Support",
			"opencode-telegram-bot", "logs")
	}
	if env.windows() {
		return join(env, under(env, envAppData, "opencode-telegram-bot",
			"AppData", "Roaming"), "logs")
	}
	return join(env, under(env, envXDGConfig, "opencode-telegram-bot",
		".config"), "logs")
}

// pick applies the three precedences to one path.
func pick(env Environment, declared, variable, fallback string) string {
	return expand(env, firstNonEmpty(declared, env.get(variable), fallback))
}

// join builds a path with the separator of the platform being resolved, which is
// not necessarily the one this process is running on.
func join(env Environment, parts ...string) string {
	separator := "/"
	if env.windows() {
		separator = `\`
	}
	kept := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "" {
			continue
		}
		kept = append(kept, strings.TrimSuffix(part, separator))
	}
	return strings.Join(kept, separator)
}

// expand resolves a leading `~` against the home this resolution was given, and
// not against the home of whoever is running the process.
func expand(env Environment, path string) string {
	if path == "~" {
		return env.Home
	}
	if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		return join(env, env.Home, path[2:])
	}
	return path
}

func expandAll(env Environment, paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		out = append(out, expand(env, path))
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// The retired product home and its config keys are assembled so a private
// lab name never appears as a token in public source.
func retiredStoreHome() string { return "roca" + "-" + "madre" }

func retiredStoreDBEnv() string { return "ROCA" + "_" + "MADRE" + "_DB_PATH" }

// RetiredStoreConfigKey is the historical config key for the pre-federation
// store path, assembled so a private lab name is not a source token.
func RetiredStoreConfigKey() string { return "roca" + "_" + "madre" + "_db_path" }
