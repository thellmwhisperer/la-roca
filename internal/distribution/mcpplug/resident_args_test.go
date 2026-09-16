package mcpplug

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
)

func TestVectorQueryPreservesNegativeLimitAlias(t *testing.T) {
	if got := (vectorQueryArgs{Limit: -1}).hitCount(); got != -1 {
		t.Fatalf("negative vector limit became %d", got)
	}
}

func TestVectorQueryIsFirstInToolDiscovery(t *testing.T) {
	dataDir := t.TempDir()
	svc, err := service.Open(service.Options{
		DBPath:  filepath.Join(dataDir, "roca.db"),
		DataDir: dataDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { svc.Close() })

	server := newServer(svc, Build{Version: "test"}, &residentVector{})
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { serverSession.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "test"}, nil)
	clientSession, err := client.Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { clientSession.Close() })

	tools, err := clientSession.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) == 0 {
		t.Fatal("tools/list returned no tools")
	}
	if tools.Tools[0].Name != vectorQueryTool.Name {
		t.Fatalf("first tool = %q, want %q", tools.Tools[0].Name, vectorQueryTool.Name)
	}
}
