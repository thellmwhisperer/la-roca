package service

import (
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/provider"
	"github.com/thellmwhisperer/la-roca/internal/provider/plugin"
)

func TestParseDatabaseList(t *testing.T) {
	for _, tc := range []struct {
		raw     string
		want    []string
		wantErr string
	}{
		{raw: "", want: nil},
		{raw: "  ", want: nil},
		{raw: "corpus", want: []string{"corpus"}},
		{raw: "corpus,ops", want: []string{"corpus", "ops"}},
		{raw: " cron, corpus ", want: []string{"cron", "corpus"}},
		{raw: "all", want: []string{"all"}},
		{raw: "all,corpus", wantErr: "cannot be combined"},
		{raw: "corpus,", wantErr: "empty database name"},
	} {
		got, err := ParseDatabaseList(tc.raw)
		if tc.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("ParseDatabaseList(%q) = %v, %v; want error %q",
					tc.raw, got, err, tc.wantErr)
			}
			continue
		}
		if err != nil || !stringSlicesEqual(got, tc.want) {
			t.Errorf("ParseDatabaseList(%q) = %v, %v; want %v", tc.raw, got, err, tc.want)
		}
	}
}

func TestResolveScopeSelectsOnlyNamedDatabases(t *testing.T) {
	corpus := plugin.Database{Descriptor: plugin.Descriptor{
		Name: rocaCorpusPluginName, DatabaseName: "corpus", Schema: "plugin_roca_corpus",
	}}
	ops := plugin.Database{Descriptor: plugin.Descriptor{
		Name: rocaOpsPluginName, DatabaseName: "ops", Schema: "plugin_roca_ops",
	}}
	inventory := pluginRoute{includeCore: true, databases: []plugin.Database{corpus, ops}}

	for _, tc := range []struct {
		name        string
		names       []string
		wantCore    bool
		wantSchemas []string
		wantUnused  []string
		wantErr     string
	}{
		{name: "default prefers corpus", wantCore: true, wantSchemas: []string{"plugin_roca_corpus"},
			wantUnused: []string{"ops"}},
		{name: "explicit pair", names: []string{"corpus", "ops"},
			wantSchemas: []string{"plugin_roca_corpus", "plugin_roca_ops"}, wantUnused: []string{"core"}},
		{name: "all attached", names: []string{"all"}, wantCore: true,
			wantSchemas: []string{"plugin_roca_corpus", "plugin_roca_ops"}},
		{name: "unknown is loud", names: []string{"nope"},
			wantErr: "unknown database \"nope\"; attached databases: core, corpus, ops"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			route, err := resolveScope(tc.names, inventory)
			if tc.wantErr != "" {
				if err == nil || err.Error() != tc.wantErr {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if route.includeCore != tc.wantCore {
				t.Fatalf("includeCore = %v, want %v", route.includeCore, tc.wantCore)
			}
			var schemas []string
			for _, database := range route.databases {
				schemas = append(schemas, database.Schema)
			}
			if !stringSlicesEqual(schemas, tc.wantSchemas) {
				t.Fatalf("schemas = %v, want %v", schemas, tc.wantSchemas)
			}
			if unused := route.unusedNames(inventory); !stringSlicesEqual(unused, tc.wantUnused) {
				t.Fatalf("unused = %v, want %v", unused, tc.wantUnused)
			}
		})
	}
}

func TestDefaultScopeWithoutCorpusIsCore(t *testing.T) {
	ops := plugin.Database{Descriptor: plugin.Descriptor{
		Name: rocaOpsPluginName, DatabaseName: "ops", Schema: "plugin_roca_ops",
	}}
	route, err := resolveScope(nil, pluginRoute{includeCore: true, databases: []plugin.Database{ops}})
	if err != nil || !route.includeCore || len(route.databases) != 0 {
		t.Fatalf("route = %+v, err = %v; want core only", route, err)
	}
}

func TestResolveDatabaseScopeUsesTheFeatureGatedRuntimeInventory(t *testing.T) {
	corpus := plugin.Database{Descriptor: plugin.Descriptor{
		Name: rocaCorpusPluginName, DatabaseName: "corpus", Schema: "plugin_roca_corpus",
	}}
	svc := Service{
		opts:             Options{CorpusEnabled: true},
		resident:         []plugin.Database{corpus},
		residentWarnings: []string{"synthetic inventory warning"},
	}
	scope, err := svc.ResolveDatabaseScope(t.Context(), []string{ScopeAll})
	if err != nil {
		t.Fatal(err)
	}
	if !stringSlicesEqual(scope.Databases, []string{"core", "corpus"}) ||
		!stringSlicesEqual(scope.Warnings, []string{"synthetic inventory warning"}) ||
		len(scope.Selected) != 2 || scope.Selected[1].Source != "plugin:roca-corpus" {
		t.Fatalf("runtime database scope = %+v", scope)
	}
}

func TestResolveDatabaseScopeKeepsDuplicateCanonicalNamesBySource(t *testing.T) {
	first := plugin.Database{Descriptor: plugin.Descriptor{
		Name: "fixture-first", DatabaseName: "shared", Schema: "plugin_fixture_first",
	}}
	second := plugin.Database{Descriptor: plugin.Descriptor{
		Name: "fixture-second", DatabaseName: "shared", Schema: "plugin_fixture_second",
	}}
	svc := Service{
		opts:     Options{CorpusEnabled: true},
		resident: []plugin.Database{first, second},
	}
	scope, err := svc.ResolveDatabaseScope(t.Context(), []string{ScopeAll})
	if err != nil {
		t.Fatal(err)
	}
	if !stringSlicesEqual(scope.Databases, []string{"core", "shared", "shared"}) ||
		len(scope.Selected) != 3 ||
		scope.Selected[1] != (DatabaseSelection{Source: "plugin:fixture-first", Database: "shared"}) ||
		scope.Selected[2] != (DatabaseSelection{Source: "plugin:fixture-second", Database: "shared"}) {
		t.Fatalf("duplicate-name database scope = %+v", scope)
	}
}

func TestWidenReplyRequiresTheExactUppercaseToken(t *testing.T) {
	for _, tc := range []struct {
		reply string
		want  bool
	}{
		{reply: "WIDEN", want: true},
		{reply: "  WIDEN\n", want: true},
		{reply: "widen"},
		{reply: "Widen"},
		{reply: "WIDEN now"},
	} {
		if got := WidenReply(tc.reply); got != tc.want {
			t.Errorf("WidenReply(%q) = %v, want %v", tc.reply, got, tc.want)
		}
	}
}

func TestOnlyEmptyUsableAnswersMayWiden(t *testing.T) {
	for _, tc := range []struct {
		name string
		res  QueryResult
		want bool
	}{
		{name: "empty rows", res: QueryResult{Path: PathLLM, Match: MatchEmpty}, want: true},
		{name: "model unavailable with empty rescue", res: QueryResult{
			Path: PathKeyword, Match: MatchEmpty, Degraded: DegradedUnavailable,
		}, want: true},
		{name: "invalid sql", res: QueryResult{
			Path: PathKeyword, Match: MatchEmpty, Degraded: DegradedInvalidSQL,
		}},
		{name: "execution failure", res: QueryResult{
			Path: PathKeyword, Match: MatchEmpty, Degraded: DegradedExecution,
		}},
		{name: "execution timeout", res: QueryResult{
			Path: PathKeyword, Match: MatchEmpty, Degraded: DegradedTimeout,
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := insufficientAnswer(tc.res); got != tc.want {
				t.Fatalf("insufficientAnswer(%+v) = %v, want %v", tc.res, got, tc.want)
			}
		})
	}
}

func TestWidenedPassKeepsOnlyCumulativeState(t *testing.T) {
	first := QueryResult{
		Question: "synthetic question", Path: PathKeyword, Message: "nothing relevant",
		Degraded: DegradedUnavailable, Match: MatchEmpty, Columns: []string{"text"},
		Rows: []map[string]any{{"text": "stale"}}, RowCount: 1,
		Providers: []provider.Attempt{{Name: "first"}}, Warnings: []string{"warning"},
		RetriedSQL: true, RetryType: RetryGateRejection, FirstModelSQL: "SELECT missing",
		RetryReason: "missing", FirstRepaired: []string{"code_fence"},
		LLMLatencyMS: 2, SQLRetryProviderLatencyMS: 1, SQLInferenceMS: 3,
		SQLRetryInferenceMS: 1, ExecutionMS: 4, Version: "v-test", SourceSHA: "abc",
	}
	got := beginWidenedPass(first, pluginRoute{includeCore: true})
	if got.Message != "" || got.Degraded != "" || got.Path != "" || got.Match != "" ||
		got.RowCount != 0 || got.Rows != nil || got.Columns != nil {
		t.Fatalf("widened pass retained stale answer state: %+v", got)
	}
	if !got.Widened || got.LLMLatencyMS != 2 || got.ExecutionMS != 4 ||
		len(got.Providers) != 1 || got.FirstModelSQL != "SELECT missing" {
		t.Fatalf("widened pass lost cumulative state: %+v", got)
	}
}

func TestMergeWidenedResultAccumulatesQueryTelemetry(t *testing.T) {
	first := QueryResult{
		Providers: []provider.Attempt{{Name: "first"}}, LLMLatencyMS: 2,
		SQLRetryProviderLatencyMS: 3, SQLInferenceMS: 5, SQLRetryInferenceMS: 7,
		ExecutionMS: 11, LatencyMS: 13, RetriedSQL: true, RetryType: RetryGateRejection,
		FirstModelSQL: "SELECT missing", RetryReason: "missing",
		FirstRepaired: []string{"code_fence"},
	}
	widened := QueryResult{
		Providers: []provider.Attempt{{Name: "second"}}, LLMLatencyMS: 17,
		SQLRetryProviderLatencyMS: 19, SQLInferenceMS: 23, SQLRetryInferenceMS: 29,
		ExecutionMS: 31, LatencyMS: 37,
	}
	got := MergeWidenedResult(first, widened)
	if !got.Widened || len(got.Providers) != 2 || got.LLMLatencyMS != 19 ||
		got.SQLRetryProviderLatencyMS != 22 || got.SQLInferenceMS != 28 ||
		got.SQLRetryInferenceMS != 36 || got.ExecutionMS != 42 || got.LatencyMS != 50 ||
		!got.RetriedSQL || got.FirstModelSQL != "SELECT missing" {
		t.Fatalf("merged telemetry = %+v", got)
	}
}

func stringSlicesEqual(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
