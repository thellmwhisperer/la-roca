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
