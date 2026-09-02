//go:build linux

package vector

import (
	"strings"
	"testing"
)

func TestLinuxProcessIdentityUsesBootIDAndRawStartTicks(t *testing.T) {
	stat := []byte("123 (vector worker) S" + strings.Repeat(" 0", 18) + " 4242\n")
	identity, err := linuxProcessIdentity(123, stat, []byte("boot-a\n"))
	if err != nil {
		t.Fatal(err)
	}
	if identity != "boot-a:4242" {
		t.Fatalf("process identity = %q", identity)
	}
	otherBoot, err := linuxProcessIdentity(123, stat, []byte("boot-b\n"))
	if err != nil {
		t.Fatal(err)
	}
	if otherBoot == identity {
		t.Fatal("process identity survived a boot identity change")
	}
}
