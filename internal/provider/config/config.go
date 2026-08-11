// Package config resolves where what La Roca writes lives.
//
// Resolution chooses a location, never a database to import. An existing
// database enters the default home only through the explicit init question.
package config

import (
	"fmt"
	"path/filepath"
)

// Input is what is known at startup, in decreasing order of precedence.
type Input struct {
	Flag string // --db-path
	Env  string // ROCA_DB_PATH
	Home string // os.UserHomeDir()
	// ConfigEnv is ROCA_CONFIG. It moves the config file without moving the
	// data, which is what a test sandbox and a machine with a shared config
	// both need.
	ConfigEnv string
}

// Paths are the resolved paths.
type Paths struct {
	Home    string
	DB      string
	Backups string
	Runner  string
	// Config is the operator's TOML. It hangs off the data directory so that an
	// imported database keeps its config next to the data it imported.
	Config string
	// Credentials is where subscription sessions live, with the permissions of
	// a secret. It never holds a platform key: those live in the config or in
	// the environment.
	Credentials string
}

// Directories and files this product knows about.
const (
	DirOwn     = ".roca"
	FileDB     = "roca.db"
	DirBackups = "backups"
	DirRunner  = "runner"
	EnvDBPath  = "ROCA_DB_PATH"
)

// Resolve decides which database is opened and where its backups and its cache
// go. La Roca's home is always ~/.roca unless the operator names another
// location explicitly.
func Resolve(in Input) (Paths, error) {
	if path := firstNonEmpty(in.Flag, in.Env); path != "" {
		abs, err := filepath.Abs(path)
		if err != nil {
			return Paths{}, fmt.Errorf("resolve the database path %q: %w", path, err)
		}
		return inDataDir(Paths{
			DB:      abs,
			Backups: filepath.Join(filepath.Dir(abs), DirBackups),
		}, filepath.Dir(abs), in), nil
	}
	if in.Home == "" {
		return Paths{}, fmt.Errorf(
			"I do not know where your HOME is: name the database with --db-path or with %s", EnvDBPath)
	}

	ownDir := filepath.Join(in.Home, DirOwn)
	return inDataDir(Paths{
		DB:      filepath.Join(ownDir, FileDB),
		Backups: filepath.Join(ownDir, DirBackups),
	}, ownDir, in), nil
}

// inDataDir hangs the configuration and credentials off the data directory.
func inDataDir(paths Paths, dataDir string, in Input) Paths {
	paths.Home = in.Home
	paths.Runner = filepath.Join(dataDir, DirRunner)
	paths.Config = filepath.Join(dataDir, FileConfig)
	if in.ConfigEnv != "" {
		paths.Config = in.ConfigEnv
	}
	paths.Credentials = filepath.Join(dataDir, DirCredentials)
	return paths
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
