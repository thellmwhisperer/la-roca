package vector

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	DatabaseFilename    = "vector.db"
	CompletionFilename  = "completion.json"
	WorkerLogFilename   = "worker.log"
	WorkerClaimFilename = ".worker"
	relocationLockFile  = ".roca-vector.relocation.lock"
)

var workerProcessAlive = processAlive

type Completion struct {
	ExitStatus int       `json:"exit_status"`
	Delta      Delta     `json:"counts"`
	Model      string    `json:"model"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	Error      string    `json:"error,omitempty"`
}

type Notifier interface {
	Notify(context.Context, Completion) error
}

type Worker struct {
	Index       Index
	DataDir     string
	PullModel   bool
	Notifier    Notifier
	WaitForCalm func(context.Context) error
}

func (w Worker) Run(ctx context.Context) Completion {
	started := time.Now().UTC()
	completion := Completion{ExitStatus: 0, Model: w.Index.Model, StartedAt: started}
	failIf := func(err error) {
		if err != nil && completion.Error == "" {
			completion.ExitStatus, completion.Error = 1, err.Error()
		}
	}
	if w.PullModel {
		failIf(w.Index.Embedder.Pull(ctx, w.Index.Model))
	}
	if completion.Error == "" && w.WaitForCalm != nil {
		failIf(w.WaitForCalm(ctx))
	}
	if completion.Error == "" {
		delta, err := w.Index.Ingest(ctx)
		completion.Delta = delta
		failIf(err)
	}
	completion.FinishedAt = time.Now().UTC()
	if err := writeCompletion(w.DataDir, completion); err != nil {
		failIf(err)
	}
	if w.Notifier != nil {
		_ = w.Notifier.Notify(ctx, completion)
	}
	return completion
}

func writeCompletion(directory string, completion Completion) error {
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(completion, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	temporary := filepath.Join(directory, ".completion.tmp")
	if err := os.WriteFile(temporary, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, filepath.Join(directory, CompletionFilename))
}

type LaunchRequest struct {
	Executable  string
	Arguments   []string
	DataDir     string
	Progress    *os.File
	Environment []string
}

type LaunchResult struct {
	PID            int    `json:"pid,omitempty"`
	LogPath        string `json:"log_path"`
	LogOffset      int64  `json:"log_offset,omitempty"`
	AlreadyRunning bool   `json:"already_running"`
}

func LockStateUsage(directory string) (func() error, error) {
	if directory == "" {
		return nil, fmt.Errorf("vector state directory is required")
	}
	managed := filepath.Base(directory) == "state" &&
		(filepath.Base(filepath.Dir(directory)) == "vector" ||
			filepath.Base(filepath.Dir(directory)) == "roca-vector")
	lockPath := filepath.Join(directory, ".relocation.lock")
	if managed {
		lockPath = filepath.Join(filepath.Dir(filepath.Dir(directory)), relocationLockFile)
	} else if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return nil, err
	}
	release, err := lockSharedFile(lockPath)
	if err != nil {
		return nil, fmt.Errorf("lock vector state relocation: %w", err)
	}
	if _, err := os.Stat(directory); err != nil {
		_ = release()
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("vector state moved from %s; rerun the command", directory)
		}
		return nil, err
	}
	return release, nil
}

func Launch(request LaunchRequest) (LaunchResult, error) {
	if request.Executable == "" || request.DataDir == "" {
		return LaunchResult{}, fmt.Errorf("vector worker executable and data directory are required")
	}
	releaseState, err := LockStateUsage(request.DataDir)
	if err != nil {
		return LaunchResult{}, err
	}
	defer releaseState()
	if err := os.MkdirAll(request.DataDir, 0o700); err != nil {
		return LaunchResult{}, err
	}
	claimPath := filepath.Join(request.DataDir, WorkerClaimFilename)
	claim, err := claimWorker(claimPath)
	if err != nil {
		return LaunchResult{}, err
	}
	if claim == nil {
		return LaunchResult{LogPath: filepath.Join(request.DataDir, WorkerLogFilename), AlreadyRunning: true}, nil
	}
	removeClaim := true
	defer func() {
		claim.Close()
		if removeClaim {
			_ = os.Remove(claimPath)
		}
	}()
	logPath := filepath.Join(request.DataDir, WorkerLogFilename)
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return LaunchResult{}, err
	}
	defer log.Close()
	logInfo, err := log.Stat()
	if err != nil {
		return LaunchResult{}, err
	}
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		return LaunchResult{}, err
	}
	defer devNull.Close()
	command := exec.Command(request.Executable, request.Arguments...)
	command.Env = append(os.Environ(), request.Environment...)
	command.Stdin, command.Stdout, command.Stderr = devNull, log, log
	if request.Progress != nil && runtime.GOOS != "windows" {
		command.ExtraFiles = []*os.File{request.Progress}
		command.Args = append(command.Args, "--progress-fd=3")
	}
	command.SysProcAttr = detachedProcessAttributes
	if err := command.Start(); err != nil {
		return LaunchResult{}, fmt.Errorf("start vector worker: %w", err)
	}
	pid := command.Process.Pid
	if _, err := fmt.Fprintf(claim, "%d\n", pid); err != nil {
		_ = command.Process.Kill()
		return LaunchResult{}, err
	}
	if err := claim.Close(); err != nil {
		_ = command.Process.Kill()
		return LaunchResult{}, err
	}
	if err := command.Process.Release(); err != nil {
		return LaunchResult{}, err
	}
	removeClaim = false
	return LaunchResult{PID: pid, LogPath: logPath, LogOffset: logInfo.Size()}, nil
}

func claimWorker(path string) (*os.File, error) {
	for attempt := 0; attempt < 2; attempt++ {
		claim, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			return claim, nil
		}
		if !os.IsExist(err) {
			return nil, fmt.Errorf("claim vector worker: %w", err)
		}
		info, statErr := os.Stat(path)
		pid := ReadWorkerPID(filepath.Dir(path))
		fresh := statErr == nil && time.Since(info.ModTime()) < 5*time.Minute
		if (pid > 0 && workerProcessAlive(pid)) || (pid == 0 && fresh) {
			return nil, nil
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("remove stale vector worker claim: %w", err)
		}
	}
	return nil, fmt.Errorf("vector worker claim changed while it was inspected")
}

func ReleaseWorkerClaim(directory string) {
	_ = os.Remove(filepath.Join(directory, WorkerClaimFilename))
}

type SystemNotifier struct{}

func (SystemNotifier) Notify(ctx context.Context, completion Completion) error {
	title := "La Roca vector indexing finished"
	message := fmt.Sprintf("exit %d · %d added · %d updated · %d removed · %d total chunks",
		completion.ExitStatus, completion.Delta.Added, completion.Delta.Updated,
		completion.Delta.Removed, completion.Delta.Chunks)
	if completion.Error != "" {
		message = fmt.Sprintf("exit %d · %s", completion.ExitStatus, completion.Error)
	}
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		script := `display notification "` + appleScript(message) + `" with title "` + appleScript(title) + `"`
		command = exec.CommandContext(ctx, "osascript", "-e", script)
	case "linux":
		if _, err := exec.LookPath("notify-send"); err != nil {
			return nil
		}
		command = exec.CommandContext(ctx, "notify-send", title, message)
	default:
		return nil
	}
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("send vector completion notification: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func appleScript(value string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", " ").Replace(value)
}

// WorkerRunning says whether a background pass is reading right now. The claim
// file is the same one Launch takes, so the answer is the process itself and not
// a status somebody remembered to write.
func WorkerRunning(directory string) bool {
	pid := ReadWorkerPID(directory)
	return pid > 0 && workerProcessAlive(pid)
}

// ReadCompletion returns the record the last pass left behind, if there is one.
func ReadCompletion(directory string) (Completion, bool) {
	raw, err := os.ReadFile(filepath.Join(directory, CompletionFilename))
	if err != nil {
		return Completion{}, false
	}
	var completion Completion
	if err := json.Unmarshal(raw, &completion); err != nil {
		return Completion{}, false
	}
	return completion, true
}

func ReadWorkerPID(directory string) int {
	raw, err := os.ReadFile(filepath.Join(directory, WorkerClaimFilename))
	if err != nil {
		return 0
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
	return pid
}
