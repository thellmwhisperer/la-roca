package mcpplug

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/distribution/logfile"
	"github.com/thellmwhisperer/la-roca/internal/provider/plugin"
)

type companionPolicy struct {
	Backoff []time.Duration
}

var defaultCompanionPolicy = companionPolicy{
	Backoff: []time.Duration{250 * time.Millisecond, time.Second, 4 * time.Second},
}

type companionRecord struct {
	Timestamp time.Time `json:"timestamp"`
	Plugin    string    `json:"plugin"`
	Event     string    `json:"event"`
	Reason    string    `json:"reason,omitempty"`
	Attempt   int       `json:"attempt,omitempty"`
}

type companionSet struct {
	mu       sync.Mutex
	closing  bool
	wg       sync.WaitGroup
	children map[string]*exec.Cmd
	stdins   []io.Closer
	logs     *logfile.Writer
	notices  io.Writer
}

func startPluginCompanions(root, dataDir string, notices io.Writer) *companionSet {
	return startPluginCompanionsWithPolicy(root, dataDir, notices, defaultCompanionPolicy)
}

func startPluginCompanionsWithPolicy(root, dataDir string, notices io.Writer, policy companionPolicy) *companionSet {
	set := &companionSet{
		children: make(map[string]*exec.Cmd),
		notices:  notices,
	}
	if dataDir != "" {
		set.logs = logfile.New(dataDir)
	}
	if root == "" {
		return set
	}
	specs, warnings := plugin.SessionCompanions(root)
	for _, warning := range warnings {
		set.notice(warning)
	}
	for _, spec := range specs {
		set.wg.Add(1)
		go set.supervise(spec, policy)
	}
	return set
}

func (s *companionSet) supervise(spec plugin.SessionCompanion, policy companionPolicy) {
	defer s.wg.Done()
	path, reason := resolveCompanionExecutable(spec.Directory, spec.Executable)
	if path == "" {
		s.notice(fmt.Sprintf("plugin %s companion did not start", spec.Plugin))
		s.log(companionRecord{Plugin: spec.Plugin, Event: "unavailable", Reason: reason})
		return
	}
	attempts := 0
	for {
		if s.isClosing() {
			return
		}
		attempts++
		err := s.runOnce(spec, path)
		if s.isClosing() {
			return
		}
		s.log(companionRecord{Plugin: spec.Plugin, Event: "exited", Reason: companionExitReason(err), Attempt: attempts})
		if err == nil {
			return
		}
		if attempts > len(policy.Backoff) {
			s.notice(fmt.Sprintf("plugin %s companion stopped", spec.Plugin))
			s.log(companionRecord{Plugin: spec.Plugin, Event: "stopped", Attempt: attempts})
			return
		}
		if s.sleep(policy.Backoff[attempts-1]) {
			return
		}
	}
}

func (s *companionSet) runOnce(spec plugin.SessionCompanion, path string) error {
	command := exec.Command(path, spec.Args...)
	command.Dir = spec.Directory
	command.Env = os.Environ()
	command.Stderr = os.Stderr
	stdin, err := command.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		stdin.Close()
		return err
	}
	if err := command.Start(); err != nil {
		stdin.Close()
		return err
	}
	s.track(spec.Plugin, command, stdin)
	go io.Copy(io.Discard, stdout)
	err = command.Wait()
	s.untrack(spec.Plugin, command)
	return err
}

func (s *companionSet) track(name string, command *exec.Cmd, stdin io.Closer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		stdin.Close()
		if command.Process != nil {
			_ = command.Process.Kill()
		}
		return
	}
	s.children[name] = command
	s.stdins = append(s.stdins, stdin)
}

func (s *companionSet) untrack(name string, command *exec.Cmd) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.children[name] == command {
		delete(s.children, name)
	}
}

func (s *companionSet) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		s.wg.Wait()
		return nil
	}
	s.closing = true
	stdins := append([]io.Closer(nil), s.stdins...)
	cmds := make([]*exec.Cmd, 0, len(s.children))
	for _, command := range s.children {
		cmds = append(cmds, command)
	}
	s.mu.Unlock()
	for _, stdin := range stdins {
		_ = stdin.Close()
	}
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		for _, command := range cmds {
			if command != nil && command.Process != nil {
				_ = command.Process.Kill()
			}
		}
		<-done
	}
	return nil
}

func (s *companionSet) running() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, command := range s.children {
		if command != nil && command.Process != nil && command.ProcessState == nil {
			return true
		}
	}
	return false
}

func (s *companionSet) isClosing() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closing
}

func (s *companionSet) sleep(delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	deadline := time.NewTicker(20 * time.Millisecond)
	defer deadline.Stop()
	for {
		if s.isClosing() {
			return true
		}
		select {
		case <-timer.C:
			return false
		case <-deadline.C:
		}
	}
}

func (s *companionSet) notice(line string) {
	if s == nil || s.notices == nil || line == "" {
		return
	}
	fmt.Fprintln(s.notices, "notice: "+line)
}

func (s *companionSet) log(record companionRecord) {
	if s == nil || s.logs == nil {
		return
	}
	if record.Timestamp.IsZero() {
		record.Timestamp = time.Now().UTC()
	}
	_ = s.logs.Append(logfile.Companions, record)
}

func resolveCompanionExecutable(directory, name string) (string, string) {
	if directory == "" || name == "" || filepath.Base(name) != name || name == "." || name == ".." ||
		strings.ContainsAny(name, "/\\\x00") {
		return "", "executable is missing"
	}
	path := filepath.Join(directory, name)
	if filepath.Base(path) != name || filepath.Clean(filepath.Dir(path)) != filepath.Clean(directory) {
		return "", "executable is missing"
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", "executable is missing"
	}
	if !info.Mode().IsRegular() {
		return "", "executable is missing"
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		return "", "executable is not executable"
	}
	return path, ""
}

func companionExitReason(err error) string {
	if err == nil {
		return "exit 0"
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return fmt.Sprintf("exit %d", exit.ExitCode())
	}
	return "exited"
}
