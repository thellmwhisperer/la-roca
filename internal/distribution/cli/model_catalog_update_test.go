package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/distribution/release"
)

func TestUpdateRefreshesTheSnapshotOnlyWhenItIsAnUpdate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, _ *http.Request) {
		out.Header().Set("Content-Type", "application/json")
		_, _ = out.Write([]byte(`{"tag_name":"v1.2.3","assets":[]}`))
	}))
	defer server.Close()
	source := release.Source{API: server.URL, Repo: "synthetic/repository", HTTP: server.Client()}

	for _, test := range []struct {
		name        string
		checkOnly   bool
		wantRefresh int
	}{
		{name: "update", wantRefresh: 1},
		{name: "check", checkOnly: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output strings.Builder
			refreshes := 0
			env := &cliEnv{
				build: Build{Version: "v1.2.3"}, out: &output, errOut: &output,
				modelCatalogRefresh: func(context.Context) error {
					refreshes++
					return nil
				},
			}
			if err := env.update(context.Background(), source, "", "", test.checkOnly); err != nil {
				t.Fatal(err)
			}
			if refreshes != test.wantRefresh {
				t.Fatalf("refreshes = %d, want %d", refreshes, test.wantRefresh)
			}
		})
	}
}
