package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// encodedTerm keeps private workflow vocabulary out of the public source while
// still letting the build reject an accidental reintroduction.
type encodedTerm struct {
	size   int
	digest string
}

var forbiddenWorkflowDigests = []encodedTerm{
	{7, "2780b8eef998d6eaca5dbfe00e4043e626c79bc214195e1848af17a85d51519c"},
	{7, "85ae1f03a63b435779b620be486c44c6c00b1c05119d777812129495b7f3fcd9"},
	{8, "012fb99445b8720cab416e6f6d64c74c52904a27fcd74110c76eb1947faa73cd"},
	{9, "6accd937d574615b71052b44f5df52a368034ab02198eff32d7212c64597608d"},
	{8, "85707e2f9af7ce7ba04fba7bf95f559b6e2a87f5dfb18182553d9bb4dc34f482"},
	{10, "fed758ce995c9a6706884ca28bbae8b94b406d0fb9b04b03eed9db17bda10939"},
	{11, "7c54ce7909371ad25942beb9605e46e93e72635a7b6b3b53c9f6a458e942424c"},
	{9, "0d7b01056c6dfd002ed80f90c355f50818ccf09eba6ec034ed316b627db58e3c"},
	{4, "311fe3feed16b9cd8df0f8b1517be5cb86048707df4889ba8dc37d4d68866d02"},
	{11, "65d91fda76fddba9fef2be5d135859ed5b4ba67d06fc6d824ae6c3e2eb81c964"},
	{10, "def818538973d347ed9e659ce6853acd03990325249f45b7a93be413c0dd9752"},
	{10, "a3a986080d63e70e8cf8a74556be320771fe374ff55d392c41c8f376ae6137a0"},
	{10, "a1b2aab7bfa97e163a31d29858a3b97ec214eacd580c5348b8d47f8f17cbd0b6"},
	{9, "a31f2e7f9f19a43bf20212a590f331382cd99750c335e110db8825c09363a25c"},
	{5, "3608e4bd0e693177369e17f48cdf750eb962b86aaac1bf6b50c7a46d52f7d94b"},
}

var nonProductDirs = map[string]bool{
	".git": true, ".tmp": true, ".worktrees": true,
	"bin": true, "dist": true, "vendor": true,
	"node_modules": true, ".opencode": true, ".claude": true,
}

func TestProductVocabularyIsFreeOfInternalRoles(t *testing.T) {
	_, here, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(here), "..", "..", ".."))

	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if nonProductDirs[entry.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Name() == ".git" || filepath.Clean(path) == filepath.Clean(here) {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.IndexByte(body, 0) >= 0 {
			return nil
		}
		if offset, ok := encodedVocabularyMatch(body, forbiddenWorkflowDigests); ok {
			t.Errorf("%s: forbidden internal vocabulary at byte %d", path, offset)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func encodedVocabularyMatch(body []byte, terms []encodedTerm) (int, bool) {
	lower := []byte(strings.ToLower(string(body)))
	bySize := make(map[int]map[[sha256.Size]byte]struct{})
	for _, term := range terms {
		raw, err := hex.DecodeString(term.digest)
		if err != nil || len(raw) != sha256.Size {
			panic(fmt.Sprintf("invalid vocabulary digest %q", term.digest))
		}
		var digest [sha256.Size]byte
		copy(digest[:], raw)
		if bySize[term.size] == nil {
			bySize[term.size] = make(map[[sha256.Size]byte]struct{})
		}
		bySize[term.size][digest] = struct{}{}
	}
	for size, digests := range bySize {
		for offset := 0; offset+size <= len(lower); offset++ {
			digest := sha256.Sum256(lower[offset : offset+size])
			if _, found := digests[digest]; found {
				return offset, true
			}
		}
	}
	return 0, false
}
