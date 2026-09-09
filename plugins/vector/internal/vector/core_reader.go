package vector

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
)

type coreReaderKey struct{}

var nextCursor atomic.Uint64

func (c CoreCLI) queryIngestCursor(ctx context.Context, statement string, cursor *string) ([]map[string]any, error) {
	ctx, cancel := boundContext(ctx, ingestPageTimeout)
	defer cancel()
	request := map[string]any{}
	if *cursor == "" {
		*cursor = strconv.FormatUint(nextCursor.Add(1), 10)
		request["sql"] = statement
	}
	request["cursor"] = *cursor
	var result execResult
	err := c.read(ctx, request, &result)
	return result.Rows, err
}

// One operation owns one lazy reader. Nested ingest/query calls share it, while
// independent resident requests see fresh routing and source visibility.
func withCoreReader(ctx context.Context) (context.Context, func()) {
	if ctx.Value(coreReaderKey{}) != nil {
		return ctx, func() {}
	}
	reader := &coreReader{lock: make(chan struct{}, 1), ctx: ctx}
	return context.WithValue(ctx, coreReaderKey{}, reader), reader.close
}

type coreReader struct {
	ctx      context.Context
	lock     chan struct{}
	command  *exec.Cmd
	cancel   context.CancelFunc
	finished func()
	input    io.WriteCloser
	encoder  *json.Encoder
	decoder  *json.Decoder
	stderr   bytes.Buffer
	err      error
}

func (r *coreReader) start(c CoreCLI) error {
	if r.command != nil || r.err != nil {
		return r.err
	}
	if strings.TrimSpace(c.Executable) == "" {
		return fmt.Errorf("roca executable is required")
	}
	finished, err := beginTrackedCommand(r.ctx)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(r.ctx)
	args := []string{"--json"}
	if c.DBPath != "" {
		args = append(args, "--db-path", c.DBPath)
	}
	args = append(args, "_vector-reader")
	command := exec.CommandContext(ctx, c.Executable, args...)
	command.Env = append(os.Environ(), "ROCA_READ_ONLY=1")
	configureCommandCancellation(command)
	command.Stderr = &r.stderr
	r.input, err = command.StdinPipe()
	var output io.ReadCloser
	if err == nil {
		output, err = command.StdoutPipe()
	}
	if err == nil {
		err = command.Start()
	}
	if err != nil {
		if r.input != nil {
			r.input.Close()
		}
		if output != nil {
			output.Close()
		}
		cancel()
		finished()
		r.err = err
		return err
	}
	r.command, r.cancel, r.finished = command, cancel, finished
	r.encoder, r.decoder = json.NewEncoder(r.input), json.NewDecoder(output)
	return nil
}

func (r *coreReader) read(ctx context.Context, c CoreCLI, request any, result any) error {
	select {
	case r.lock <- struct{}{}:
		defer func() { <-r.lock }()
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := r.start(c); err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, r.cancel)
	defer stop()
	err := r.encoder.Encode(request)
	var response struct {
		Result json.RawMessage `json:"result"`
		Error  string          `json:"error"`
	}
	if err == nil {
		err = r.decoder.Decode(&response)
	}
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err != nil {
		r.err = err
		r.stop()
		message := strings.TrimSpace(r.stderr.String())
		if len(message) > 4096 {
			message = message[:4096]
		}
		return fmt.Errorf("roca reader: %w: %s", err, message)
	}
	if response.Error != "" {
		return fmt.Errorf("roca reader: %s", response.Error)
	}
	decoder := json.NewDecoder(bytes.NewReader(response.Result))
	decoder.UseNumber()
	return decoder.Decode(result)
}

func (r *coreReader) close() {
	r.lock <- struct{}{}
	defer func() { <-r.lock }()
	r.stop()
}

func (r *coreReader) stop() {
	if r.command != nil {
		r.input.Close()
		r.cancel()
		r.command.Wait()
		r.finished()
		r.command = nil
	}
}

func (c CoreCLI) read(ctx context.Context, request map[string]any, result any) error {
	if c.readRequest != nil {
		return c.readRequest(ctx, c, request, result)
	}
	ctx, close := withCoreReader(ctx)
	defer close()
	return ctx.Value(coreReaderKey{}).(*coreReader).read(ctx, c, request, result)
}
