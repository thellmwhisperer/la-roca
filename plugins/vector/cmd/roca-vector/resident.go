package main

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/thellmwhisperer/la-roca-vector/internal/engine"
	"github.com/thellmwhisperer/la-roca-vector/internal/vector"
)

type residentRequest struct {
	ID        int64  `json:"id"`
	Op        string `json:"op"`
	Query     string `json:"query"`
	K         int    `json:"k"`
	Databases string `json:"databases,omitempty"`
}

func residentCommand(env *environment) *cobra.Command {
	return &cobra.Command{
		Use:    "_resident",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			embedder, _ := env.embedder()
			encoder := json.NewEncoder(os.Stdout)
			started := time.Now()
			if err := encoder.Encode(engine.Progress("prewarm", "semantic search: preparing", 0, 1, 0)); err != nil {
				return err
			}
			if err := prewarmEmbedder(command.Context(), embedder); err != nil {
				_ = encoder.Encode(engine.Error("prewarm", productError(err)))
			} else {
				event := engine.Result("prewarm", "semantic search: ready")
				event.Extra = map[string]any{"prewarm_ms": time.Since(started).Milliseconds()}
				if err := encoder.Encode(event); err != nil {
					return err
				}
			}
			federation, err := env.federation("")
			if err != nil {
				return err
			}
			scanner := bufio.NewScanner(os.Stdin)
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
				result, queryErr := federation.Query(command.Context(), request.Query, request.K, request.Databases)
				response := map[string]any{
					"kind": engine.KindResult, "stage": "query", "id": request.ID,
					"elapsed_ms": queryStarted.Sub(queryStarted).Milliseconds(), "result": result,
				}
				response["elapsed_ms"] = time.Since(queryStarted).Milliseconds()
				if queryErr != nil {
					response["kind"] = engine.KindError
					response["error"] = productError(queryErr)
					response["message"] = productError(queryErr)
				}
				if err := encoder.Encode(response); err != nil {
					return err
				}
			}
			return scanner.Err()
		},
	}
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
