package cli

import (
	"context"
	"io"
	"slices"
)

func hermeticCLIEnv(env *cliEnv) *cliEnv {
	env.skipReconciliation = true
	env.modelBackend = testModelBackend{}
	env.modelPicker = testModelPicker
	env.modelCatalogRefresh = func(context.Context) error { return nil }
	return env
}

type testModelBackend struct{}

func (testModelBackend) Catalogue(_ context.Context, _, current string) (modelCatalogue, error) {
	models := []string{
		"claude-test",
		"deepseek-chat", "gpt-5.6-luna", "gpt-5.6-sol", "grok-4", "grok-chosen",
		"internal-7b", "internal-9b", "qwen3.5:4b",
	}
	if current != "" && !slices.Contains(models, current) {
		models = append(models, current)
	}
	return modelCatalogue{IDs: canonicalModelIDs(models)}, nil
}

func (testModelBackend) Probe(context.Context, string, string) error { return nil }

func testModelPicker(_ io.Reader, _ io.Writer, models []string, current string) (string, error) {
	if slices.Contains(models, current) {
		return current, nil
	}
	return models[0], nil
}
