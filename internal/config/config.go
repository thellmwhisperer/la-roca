// Package config resolves where what La Roca writes lives.
//
// Resolution chooses a location, never a database to import. An existing
// database enters the default home only through the explicit init question.
package config

import (
	"crypto/sha256"
	"encoding/hex"
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
	// Cache is the classifier's working directory for this database. It is
	// keyed by database because what is taught lives in the database: two
	// different databases have two different models, and sharing a cache would
	// mix them.
	Cache string
	// CacheRoot is the directory those per-database caches hang off. It is a
	// field of its own because the uninstall's inventory has to be able to name
	// it: Roca creates it, and a purge that only declares the keyed
	// subdirectory leaves the parent behind on every machine that ever trained
	// a classifier, and then reports it as somebody else's file.
	CacheRoot string
	// Config is the operator's TOML. It hangs off the data directory so that an
	// imported database keeps its config next to the data it imported.
	Config string
	// Credentials is where subscription sessions live, with the permissions of
	// a secret. It never holds a platform key: those live in the config or in
	// the environment.
	Credentials string
	// Language is the optional vocabulary overlay: a language.yaml the operator
	// drops in to extend the embedded pack (Italian markers, extra stop words)
	// without recompiling. Empty when the operator ships none.
	Language string
}

// Directories and files this product knows about.
const (
	DirOwn     = ".roca"
	FileDB     = "roca.db"
	DirBackups = "backups"
	DirCache   = "cache"
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

// inDataDir hangs off the data directory everything that is not the database:
// the cache, the config and the credentials.
//
// The cache goes in a subdirectory named with the fingerprint of the database
// path, so that two databases in the same directory do not share a model. The
// config and the credentials do not: they belong to the installation, not to
// one database.
func inDataDir(paths Paths, dataDir string, in Input) Paths {
	paths.Home = in.Home
	fingerprint := sha256.Sum256([]byte(paths.DB))
	paths.CacheRoot = filepath.Join(dataDir, DirCache)
	paths.Cache = filepath.Join(paths.CacheRoot, hex.EncodeToString(fingerprint[:6]))
	paths.Config = filepath.Join(dataDir, FileConfig)
	if in.ConfigEnv != "" {
		paths.Config = in.ConfigEnv
	}
	paths.Credentials = filepath.Join(dataDir, DirCredentials)
	paths.Language = filepath.Join(dataDir, FileLanguage)
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
