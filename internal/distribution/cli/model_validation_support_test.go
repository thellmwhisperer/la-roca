/**
 * @overview Supplies hermetic model validation to legacy CLI tests. ~65 lines, no public symbols.
 *
 *   READING GUIDE
 *   -------------
 *   1. Start at hermeticCLIEnv
 *   2. testModelBackend keeps unrelated command tests offline
 *   3. testModelPicker selects the already resolved default
 *
 *   MAIN FLOW
 *   test helper -> fixed catalogue -> accepting probe -> command contract
 *
 *   PUBLIC API
 *   ----------
 *   None (test-only file)
 *
 *   INTERNALS
 *   ---------
 *   hermeticCLIEnv, testModelBackend, testModelPicker
 *
 * @exports
 * @deps context, io; provider/config CLI test seams
 */
package cli

import (
	"context"
	"io"
	"slices"
)

// -- 1/2 CORE · hermeticCLIEnv <- START HERE --

func hermeticCLIEnv(env *cliEnv) *cliEnv {
	env.modelBackend = testModelBackend{}
	env.modelPicker = testModelPicker
	env.modelCatalogRefresh = func(context.Context) error { return nil }
	return env
}

// -/ 1/2

// -- 2/2 HELPER · testModelBackend and testModelPicker --

type testModelBackend struct{}

func (testModelBackend) Catalogue(_ context.Context, _, current string) (modelCatalogue, error) {
	models := []string{
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

// -/ 2/2
