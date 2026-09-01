package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode"
)

type zcodeFileIdentity struct {
	clean    string
	resolved string
	info     os.FileInfo
}

func zcodeHookCommandsReferenceWrapper(commands []string, wrapperPath string) (bool, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return false, err
	}
	wrapper, err := zcodeFileIdentityFor(wrapperPath, home)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("resolve wrapper %s: %w", wrapperPath, err)
	}
	for _, command := range commands {
		words, ok := simpleShellWords(command)
		if !ok {
			return false, fmt.Errorf("unsupported shell syntax in %q", command)
		}
		for index, word := range words {
			nestedFragment := index > 0 && zcodeShellCommandOption(words[index-1])
			candidate, relevant, err := zcodeCommandWordPath(
				word, index, nestedFragment, home, filepath.Base(wrapper.clean))
			if err != nil {
				return false, fmt.Errorf("resolve %q in hook command %q: %w", word, command, err)
			}
			if !relevant {
				continue
			}
			same, err := sameZcodeFile(candidate, wrapper, home)
			if err != nil {
				return false, fmt.Errorf("compare %q in hook command %q: %w", word, command, err)
			}
			if same {
				return true, nil
			}
		}
	}
	return false, nil
}

func zcodeCommandWordPath(word string, index int, nestedFragment bool, home, wrapperBase string) (string, bool, error) {
	if word == "" {
		return "", false, nil
	}
	if strings.HasPrefix(word, "-") {
		if strings.ContainsAny(word, `/~\\`) || strings.Contains(word, wrapperBase) {
			return "", false, fmt.Errorf("option may contain a wrapper path")
		}
		return "", false, nil
	}
	if index == 0 && strings.Contains(word, "=") && !strings.ContainsAny(word, `/\\`) {
		return "", false, fmt.Errorf("environment assignment is ambiguous")
	}
	expanded, err := expandZcodeCommandHome(word, home)
	if err != nil {
		return "", false, err
	}
	if filepath.IsAbs(expanded) {
		if zcodeAbsoluteWordNeedsProof(expanded) {
			if nestedFragment {
				return "", false, fmt.Errorf("absolute shell word is not a proven standalone path")
			}
			if _, err := os.Lstat(expanded); err != nil {
				if os.IsNotExist(err) {
					return "", false, fmt.Errorf("absolute shell word is not a proven standalone path")
				}
				return "", false, err
			}
		}
		return expanded, true, nil
	}
	if strings.ContainsAny(expanded, `/\\`) {
		return "", false, fmt.Errorf("relative path is ambiguous")
	}
	located, err := exec.LookPath(expanded)
	if err == nil {
		if !filepath.IsAbs(located) {
			return "", false, fmt.Errorf("PATH resolved to a relative path")
		}
		return located, true, nil
	}
	if expanded == wrapperBase {
		return "", false, fmt.Errorf("wrapper basename is not resolvable on PATH")
	}
	return "", false, nil
}

func zcodeShellCommandOption(word string) bool {
	if word == "--command" {
		return true
	}
	return strings.HasPrefix(word, "-") && !strings.HasPrefix(word, "--") && strings.Contains(word[1:], "c")
}

func zcodeAbsoluteWordNeedsProof(word string) bool {
	return strings.IndexFunc(word, unicode.IsSpace) >= 0 || strings.ContainsAny(word, ";&|<>()*?[")
}

func expandZcodeCommandHome(path, home string) (string, error) {
	switch {
	case path == "~":
		return home, nil
	case strings.HasPrefix(path, "~/"):
		return filepath.Join(home, strings.TrimPrefix(path, "~/")), nil
	case path == "$HOME":
		return home, nil
	case strings.HasPrefix(path, "$HOME/"):
		return filepath.Join(home, strings.TrimPrefix(path, "$HOME/")), nil
	case path == "${HOME}":
		return home, nil
	case strings.HasPrefix(path, "${HOME}/"):
		return filepath.Join(home, strings.TrimPrefix(path, "${HOME}/")), nil
	case strings.HasPrefix(path, "~") || strings.ContainsAny(path, "$`"):
		return "", fmt.Errorf("unsupported home or shell expansion")
	default:
		return filepath.Clean(path), nil
	}
}

func zcodeFileIdentityFor(path, home string) (zcodeFileIdentity, error) {
	expanded, err := expandZcodeCommandHome(path, home)
	if err != nil {
		return zcodeFileIdentity{}, err
	}
	if !filepath.IsAbs(expanded) {
		return zcodeFileIdentity{}, fmt.Errorf("path is not absolute")
	}
	clean := filepath.Clean(expanded)
	resolved, err := filepath.EvalSymlinks(clean)
	if err != nil {
		return zcodeFileIdentity{}, err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return zcodeFileIdentity{}, err
	}
	return zcodeFileIdentity{clean: clean, resolved: filepath.Clean(resolved), info: info}, nil
}

func sameZcodeFile(path string, wrapper zcodeFileIdentity, home string) (bool, error) {
	expanded, err := expandZcodeCommandHome(path, home)
	if err != nil {
		return false, err
	}
	clean := filepath.Clean(expanded)
	if clean == wrapper.clean {
		return true, nil
	}
	resolved, err := filepath.EvalSymlinks(clean)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	resolved = filepath.Clean(resolved)
	if resolved == wrapper.resolved {
		return true, nil
	}
	info, err := os.Stat(resolved)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return os.SameFile(info, wrapper.info), nil
}

func simpleShellWords(command string) ([]string, bool) {
	var words []string
	var word strings.Builder
	var quote rune
	started := false
	flush := func() {
		if started {
			words = append(words, word.String())
			word.Reset()
			started = false
		}
	}
	runes := []rune(command)
	for index := 0; index < len(runes); index++ {
		current := runes[index]
		if quote != 0 {
			if current == quote {
				quote = 0
				continue
			}
			if quote == '"' && current == '\\' {
				index++
				if index >= len(runes) || !strings.ContainsRune("$`\"\\\n", runes[index]) {
					return nil, false
				}
				if runes[index] != '\n' {
					word.WriteRune(runes[index])
					started = true
				}
				continue
			}
			if current == '`' {
				return nil, false
			}
			word.WriteRune(current)
			started = true
			continue
		}
		switch {
		case unicode.IsSpace(current):
			flush()
		case current == '#' && !started:
			flush()
			for index < len(runes) && runes[index] != '\n' {
				index++
			}
		case current == '\'' || current == '"':
			quote = current
			started = true
		case current == '\\':
			index++
			if index >= len(runes) {
				return nil, false
			}
			word.WriteRune(runes[index])
			started = true
		case strings.ContainsRune(";&|<>()`*?[", current):
			return nil, false
		default:
			word.WriteRune(current)
			started = true
		}
	}
	if quote != 0 {
		return nil, false
	}
	flush()
	return words, true
}
