package service_test

import (
	"os"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/provider/service"
)

func TestPluginVectorQueryUsesConfiguredCore(t *testing.T) {
	for _, tc := range []struct {
		name, core, environment, want string
	}{
		{"unconfigured", "", "", "unset"},
		{"configured", "selected core", "", "selected core"},
		{"override", "selected core", "explicit core", "explicit core"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			installVectorScript(t, `#!/bin/sh
[ "${ROCA_VECTOR_ROCA_BINARY-unset}" = "$EXPECTED_ROCA_BINARY" ] || exit 1
printf '%s\n' '{"vector_executed":true}'
`)
			t.Setenv("ROCA_VECTOR_ROCA_BINARY", tc.environment)
			if tc.environment == "" {
				if err := os.Unsetenv("ROCA_VECTOR_ROCA_BINARY"); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("EXPECTED_ROCA_BINARY", tc.want)
			hits, err := service.PluginVectorQuery(t.Context(), "", "harbor lantern", service.VectorLeg{
				CoreBinary: tc.core, K: 1,
			})
			if err != nil || !hits.Executed {
				t.Fatalf("core executable handoff: hits=%+v err=%v", hits, err)
			}
		})
	}
}
