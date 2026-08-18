package supportreport

import "testing"

func TestPluginOriginRedactsFilesystemSources(t *testing.T) {
	cases := []struct {
		source, origin, reported string
	}{
		{"bundled:roca", OriginBundled, "bundled:roca"},
		{"owner/repo", OriginExternal, "owner/repo"},
		{"https://example.com/plugin.git", OriginExternal, "https://example.com/plugin.git"},
		{"/Users/javiermellado/src/plugin", OriginExternal, OriginLocalDirectory},
		{"./relative", OriginExternal, OriginLocalDirectory},
	}
	for _, tc := range cases {
		origin, reported := PluginOrigin(tc.source)
		if origin != tc.origin || reported != tc.reported {
			t.Errorf("PluginOrigin(%q) = %q %q, want %q %q",
				tc.source, origin, reported, tc.origin, tc.reported)
		}
	}
}
