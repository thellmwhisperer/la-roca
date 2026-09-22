package securefile

import "os"

// FileIdentity identifies the filesystem object published by a write. It is
// intentionally opaque: callers must compare it with the path they want to
// clean up instead of sampling that path after publication and accidentally
// claiming a concurrent, byte-identical replacement.
type FileIdentity struct {
	info os.FileInfo
}

// Valid reports whether the identity came from a successful publication.
func (identity FileIdentity) Valid() bool { return identity.info != nil }

// Matches reports whether path still names the object that was published. A
// false result is a refusal signal, not evidence that path is safe to remove.
func (identity FileIdentity) Matches(path string) bool {
	if !identity.Valid() {
		return false
	}
	current, err := os.Lstat(path)
	return err == nil && os.SameFile(identity.info, current)
}

func identityFromInfo(info os.FileInfo) FileIdentity {
	return FileIdentity{info: info}
}

// Publication is the authoritative result of a write. Identity remains valid
// even when the final directory sync reports an error: the file rename already
// happened and callers must not infer ownership by inspecting the path later.
type Publication struct {
	Identity FileIdentity
}
