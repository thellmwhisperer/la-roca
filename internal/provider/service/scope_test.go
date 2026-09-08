package service

import (
	"strings"
	"testing"

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
	inventory := PluginRoute{IncludeCore: true, Databases: []plugin.Database{corpus, ops}}

	for _, tc := range []struct {
		name        string
		names       []string
		wantCore    bool
		wantSchemas []string
		wantUnused  []string
		wantErr     string
	}{
		{name: "default is the whole federation", wantCore: true,
			wantSchemas: []string{"plugin_roca_corpus", "plugin_roca_ops"}},
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
			if route.IncludeCore != tc.wantCore {
				t.Fatalf("includeCore = %v, want %v", route.IncludeCore, tc.wantCore)
			}
			var schemas []string
			for _, database := range route.Databases {
				schemas = append(schemas, database.Schema)
			}
			if !stringSlicesEqual(schemas, tc.wantSchemas) {
				t.Fatalf("schemas = %v, want %v", schemas, tc.wantSchemas)
			}
			if unused := route.UnusedNames(inventory); !stringSlicesEqual(unused, tc.wantUnused) {
				t.Fatalf("unused = %v, want %v", unused, tc.wantUnused)
			}
		})
	}
}

func TestDefaultScopeWithoutCorpusStillIncludesOps(t *testing.T) {
	ops := plugin.Database{Descriptor: plugin.Descriptor{
		Name: rocaOpsPluginName, DatabaseName: "ops", Schema: "plugin_roca_ops",
	}}
	route, err := resolveScope(nil, PluginRoute{IncludeCore: true, Databases: []plugin.Database{ops}})
	if err != nil || !route.IncludeCore || len(route.Databases) != 1 ||
		route.Databases[0].Schema != "plugin_roca_ops" {
		t.Fatalf("route = %+v, err = %v; want core and ops", route, err)
	}
}

func resolveAllDatabaseScope(t *testing.T, svc *Service) DatabaseScope {
	t.Helper()
	scope, err := svc.ResolveDatabaseScope(t.Context(), []string{ScopeAll})
	if err != nil {
		t.Fatal(err)
	}
	return scope
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
	scope := resolveAllDatabaseScope(t, &svc)
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
	scope := resolveAllDatabaseScope(t, &svc)
	if !stringSlicesEqual(scope.Databases, []string{"core", "shared", "shared"}) ||
		len(scope.Selected) != 3 ||
		scope.Selected[1] != (DatabaseSelection{Source: "plugin:fixture-first", Database: "shared"}) ||
		scope.Selected[2] != (DatabaseSelection{Source: "plugin:fixture-second", Database: "shared"}) {
		t.Fatalf("duplicate-name database scope = %+v", scope)
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
