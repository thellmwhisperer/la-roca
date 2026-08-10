package ingest

import "strings"

// The roots are configuration, never constants (TECH-SPEC 5.1). They come from
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
	PiSessions            string
	HermesDB              string
	// WorkspaceRoots resolve project identity from encoded session paths. Files
	// under them are never ingested as content.
	WorkspaceRoots []string
	// SubagentRoots are the Claude projects roots the subagent transcripts are
	// discovered under. Empty means the resolved Claude projects root.
	SubagentRoots []string
}

// Roots are the resolved locations of every source in the v1 matrix.
type Roots struct {
	ClaudeProjects        string
	ClaudeDesktopSessions string
	CoworkSessions        string
	CodexRoot             string
	CodexSessions         string
	// CodexStateDB is Codex's own SQLite state. It is read only to enrich a
	// session with the model and the agent nickname it ran under.
	CodexStateDB  string
	OpenCodeDB    string
	PiSessions    string
	HermesDB      string
	SubagentRoots []string
	Workspace     WorkspaceRoots
}

// The environment variables the laboratory already honours, kept by name so an
// operator who set them does not have to learn new ones.
const (
	envClaudeProjects = "CLAUDE_PROJECTS_ROOT"
	envCodexRoot      = "CODEX_ROOT"
	envCodexSessions  = "CODEX_SESSIONS_ROOT"
	envCodexStateDB   = "CODEX_STATE_DB_PATH"
	envOpenCodeDB     = "OPENCODE_DB_PATH"
	envPiSessions     = "PI_SESSIONS_ROOT"
	envHermesDB       = "HERMES_DB_PATH"
	envXDGConfig      = "XDG_CONFIG_HOME"
	envXDGData        = "XDG_DATA_HOME"
	envAppData        = "APPDATA"
	envLocalAppData   = "LOCALAPPDATA"
)

// ResolveRoots decides where every source lives on this machine.
func ResolveRoots(env Environment, settings Settings) Roots {
	claude := join(env, env.Home, ".claude")
	codexRoot := pick(env, settings.CodexRoot, envCodexRoot, join(env, env.Home, ".codex"))
	appSupport := claudeAppSupport(env)

	roots := Roots{
		ClaudeProjects: pick(env, settings.ClaudeProjects, envClaudeProjects,
			join(env, claude, "projects")),
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
		PiSessions: pick(env, settings.PiSessions, envPiSessions,
			join(env, env.Home, ".pi", "agent", "sessions")),
		HermesDB: pick(env, settings.HermesDB, envHermesDB,
			join(env, env.Home, ".hermes", "state.db")),
		Workspace: ResolveWorkspaceRoots(expandAll(env, settings.WorkspaceRoots)),
	}

	roots.SubagentRoots = expandAll(env, settings.SubagentRoots)
	if len(roots.SubagentRoots) == 0 {
		roots.SubagentRoots = []string{roots.ClaudeProjects}
	}
	return roots
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
