package vectorhelp_test

import (
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/pkg/vectorhelp"
)

func TestQueryReadsAHitAndOffersToNarrow(t *testing.T) {
	for _, tc := range []struct {
		name string
		hits []vectorhelp.Hit
		want string
	}{
		{
			name: "quoted identifiers",
			hits: []vectorhelp.Hit{{
				Alias: "plugin_fixture_records", Table: "order", ID: "$(touch /tmp/pwned)",
				IDColumn: "where", TextColumns: []string{"select", "from"},
			}},
			want: `roca exec 'SELECT "select", "from" FROM "plugin_fixture_records"."order" WHERE "where" = '\''$(touch /tmp/pwned)'\''' --max-chars 2000`,
		},
		{
			name: "escaped id and named columns",
			hits: []vectorhelp.Hit{{
				Alias: "plugin_fixture_records", Table: "records", ID: "a'b",
				IDColumn: "record_key", TextColumns: []string{"body", "title"},
			}},
			want: `roca exec 'SELECT "body", "title" FROM "plugin_fixture_records"."records" WHERE "record_key" = '\''a'\'''\''b'\''' --max-chars 2000`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			help := vectorhelp.Query(tc.hits)
			joined := strings.Join(help, "\n")
			if !strings.Contains(joined, tc.want) || !strings.Contains(joined, "--databases <one>") {
				t.Fatalf("vector query help = %v", help)
			}
		})
	}
}
