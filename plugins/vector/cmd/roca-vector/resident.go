package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/thellmwhisperer/la-roca-vector/internal/engine"
	"github.com/thellmwhisperer/la-roca-vector/internal/model"
	"github.com/thellmwhisperer/la-roca-vector/internal/vector"
)

const defaultResidentIdle = 5 * time.Minute

var errResidentUnusable = errors.New("semantic search resident is unusable")

type residentRequest struct {
	ID              int64   `json:"id"`
	Op              string  `json:"op"`
	Query           string  `json:"query"`
	K               int     `json:"k"`
	Databases       string  `json:"databases,omitempty"`
	ExpandTemplates bool    `json:"expand_templates,omitempty"`
	MinScore        float64 `json:"min_score,omitempty"`
}

type residentSession struct {
	waitReady func(context.Context) error
	query     func(context.Context, residentRequest) (any, error)
	extra     map[string]any
}

func residentCommand(env *environment) *cobra.Command {
	listen := ""
	idle := defaultResidentIdle
	command := &cobra.Command{
		Use:    "_resident",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if !command.Flags().Changed("idle") {
				if parsed, err := parseResidentIdle(os.Getenv("ROCA_VECTOR_RESIDENT_IDLE")); err == nil && parsed > 0 {
					idle = parsed
				}
			}
			if socket := strings.TrimSpace(listen); socket != "" {
				return runSharedResident(command.Context(), env, socket, idle)
			}
			session, err := newResidentSession(command.Context(), env)
			if err != nil {
				return err
			}
			return serveResidentSession(command.Context(), struct {
				io.Reader
				io.Writer
			}{os.Stdin, os.Stdout}, session)
		},
	}
	command.Flags().StringVar(&listen, "listen", "", "unix socket for the shared embedding resident")
	command.Flags().DurationVar(&idle, "idle", defaultResidentIdle, "exit after this idle with no clients")
	_ = command.Flags().MarkHidden("listen")
	_ = command.Flags().MarkHidden("idle")
	return command
}

func parseResidentIdle(value string) (time.Duration, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, io.EOF
	}
	return time.ParseDuration(value)
}

func newResidentSession(ctx context.Context, env *environment) (residentSession, error) {
	if err := env.startBackgroundSetup(); err != nil {
		return residentSession{}, err
	}
	embedder, events := env.queryEmbedder()
	return residentSessionWithEmbedder(ctx, env, embedder, events)
}

func residentSessionWithEmbedder(ctx context.Context, env *environment, embedder vector.Embedder, events engine.Sink) (residentSession, error) {
	started := time.Now()
	if err := prewarmEmbedder(ctx, embedder); err != nil && !errors.Is(err, model.ErrNotDownloaded) {
		return residentSession{}, err
	}
	extra := map[string]any{"prewarm_ms": time.Since(started).Milliseconds()}
	if reporter, ok := embedder.(interface{ Accelerated() bool }); ok {
		extra["accelerated"] = reporter.Accelerated()
	}
	return residentSession{
		waitReady: func(context.Context) error { return nil },
		extra:     extra,
		query: func(ctx context.Context, request residentRequest) (any, error) {
			federation, err := env.federationWithEmbedder("", embedder, events)
			if err != nil {
				return nil, err
			}
			var result vector.FederatedQuery
			var queryErr error
			if request.ExpandTemplates {
				result, queryErr = federation.QueryExpanded(ctx, request.Query, request.K, request.Databases, request.MinScore)
			} else {
				result, queryErr = federation.Query(ctx, request.Query, request.K, request.Databases)
			}
			if terminalErr := residentTerminalError(embedder); terminalErr != nil {
				return result, fmt.Errorf("%w: %w", errResidentUnusable, terminalErr)
			}
			return result, queryErr
		},
	}, nil
}

func serveResidentSession(ctx context.Context, rw io.ReadWriter, session residentSession) error {
	encoder := json.NewEncoder(rw)
	if err := encoder.Encode(engine.Progress("prewarm", "semantic search: preparing", 0, 1, 0)); err != nil {
		return err
	}
	if err := session.waitReady(ctx); err != nil {
		if encodeErr := encoder.Encode(engine.Error("prewarm", productError(err))); encodeErr != nil {
			return encodeErr
		}
		return fmt.Errorf("%w: %w", errResidentUnusable, err)
	} else {
		event := engine.Result("prewarm", "semantic search: ready")
		event.Extra = make(map[string]any, len(session.extra)+1)
		for key, value := range session.extra {
			event.Extra[key] = value
		}
		event.Extra["query_options"] = []string{"expand_templates", "min_score"}
		if err := encoder.Encode(event); err != nil {
			return err
		}
	}
	scanner := bufio.NewScanner(rw)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var request residentRequest
		if err := json.Unmarshal([]byte(line), &request); err != nil {
			_ = encoder.Encode(engine.Error("query", err.Error()))
			continue
		}
		if request.K == 0 {
			request.K = 10
		}
		queryStarted := time.Now()
		result, queryErr := session.query(ctx, request)
		elapsed := time.Since(queryStarted).Milliseconds()
		fmt.Fprintf(os.Stderr, "semantic search: answered query in %dms\n", elapsed)
		response := map[string]any{
			"kind": engine.KindResult, "stage": "query", "id": request.ID,
			"elapsed_ms": elapsed, "result": result,
		}
		if queryErr != nil {
			response["kind"] = engine.KindError
			response["error"] = productError(queryErr)
			response["message"] = productError(queryErr)
		}
		encodeErr := encoder.Encode(response)
		if errors.Is(queryErr, errResidentUnusable) {
			return queryErr
		}
		if encodeErr != nil {
			return encodeErr
		}
	}
	return scanner.Err()
}

func residentTerminalError(embedder vector.Embedder) error {
	if reporter, ok := embedder.(interface{ TerminalError() error }); ok {
		return reporter.TerminalError()
	}
	return nil
}

func prewarmEmbedder(ctx context.Context, embedder vector.Embedder) error {
	if warmer, ok := embedder.(interface{ Prewarm(context.Context) error }); ok {
		return warmer.Prewarm(ctx)
	}
	_, err := embedder.Embed(ctx, vector.DefaultModel, []string{vector.QueryPrefix + "warmup"})
	return err
}

func productError(err error) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	for _, leaked := range []string{"ollama", "gguf", "llama.cpp", "metal"} {
		if strings.Contains(strings.ToLower(message), leaked) {
			return "semantic search is not ready"
		}
	}
	return message
}
