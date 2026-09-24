package cli

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestRenderVectorDoctorSurfacesCompactAndHeldLock(t *testing.T) {
	var out bytes.Buffer
	env := &cliEnv{out: &out, errOut: io.Discard}
	chunks := int64(2)
	bytes := int64(300000)
	renderVectorDoctor(env, &vectorDoctorReport{
		Databases: []vectorDoctorDatabase{
			{
				Plugin: "roca-corpus", Database: "corpus", EmbeddedChunks: &chunks,
				SidecarBytes: &bytes, State: "complete", IndexLock: "held",
				CompactRecommended: true,
			},
		},
		Remedies: []string{
			"Run `roca vector compact` to reclaim empty embedding pages on roca-corpus/corpus",
			"index.lock is held on roca-corpus/corpus",
		},
	})
	got := out.String()
	for _, want := range []string{
		"vector sidecars:",
		"roca-corpus/corpus",
		"compact recommended",
		"lock held",
		"Run `roca vector compact`",
		"index.lock is held",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("doctor vector narration missing %q:\n%s", want, got)
		}
	}
}
