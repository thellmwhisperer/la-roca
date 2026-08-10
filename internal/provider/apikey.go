package provider

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/thellmwhisperer/la-roca/internal/securefile"
)

// APIKeyFile is the name of a key-based provider's credential inside the
// credentials directory. The suffix keeps it apart from the subscription
// session files (codex.json).
func APIKeyFile(name string) string {
	return normalize(name) + ".key"
}

// APIKeyPath is where a key-based provider's credential lives.
func APIKeyPath(credentials, name string) string {
	return filepath.Join(credentials, APIKeyFile(name))
}

// SaveAPIKey writes the key with the permissions of a secret, creating the
// directory with the permissions of a secret's directory.
func SaveAPIKey(credentials, name, key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return fmt.Errorf("the API key is empty")
	}
	path := APIKeyPath(credentials, name)
	if err := securefile.Write(path, append([]byte(key), '\n'), 0o600, 0o700); err != nil {
		return fmt.Errorf("write the credential: %w", err)
	}
	return nil
}

// LoadAPIKey reads a key-based provider's credential. A missing file is not an
// error: it is the normal case of a provider nobody has logged in to yet.
func LoadAPIKey(credentials, name string) (string, error) {
	raw, err := os.ReadFile(APIKeyPath(credentials, name))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(raw)), nil
}

// DeleteAPIKey forgets a key-based provider's credential. Deleting what is not
// there is not a failure: the end state is the one asked for.
func DeleteAPIKey(credentials, name string) error {
	err := os.Remove(APIKeyPath(credentials, name))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// KeyProviders are the presets this build logs in to with an API key, in a
// stable order. Subscription providers are not in this list.
func KeyProviders() []string { return PresetNames() }

// IsKeyProvider says whether this build knows how to store a key for name.
func IsKeyProvider(name string) bool {
	_, ok := Preset(name)
	return ok
}
