/**
 * @overview Pins update-time model snapshot refresh semantics. ~75 lines, no public symbols.
 *
 *   READING GUIDE
 *   -------------
 *   1. Start at TestUpdateRefreshesTheSnapshotOnlyWhenItIsAnUpdate
 *   2. The fake release channel always reports the installed version
 *   3. The table distinguishes update from read-only --check
 *
 *   MAIN FLOW
 *   fake release -> update/check -> optional catalogue refresh -> assertion
 *
 *   PUBLIC API
 *   ----------
 *   None (test-only file)
 *
 *   INTERNALS
 *   ---------
 *   TestUpdateRefreshesTheSnapshotOnlyWhenItIsAnUpdate
 *
 * @exports
 * @deps context, httptest, testing; distribution/release and CLI update
 */
package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/distribution/release"
)

// -- 1/1 CORE · TestUpdateRefreshesTheSnapshotOnlyWhenItIsAnUpdate <- START HERE --

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

// -/ 1/1
