package tools

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/DeusData/codebase-memory-mcp/internal/store"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type methodCountingTransport struct {
	delegate mcp.Transport
	method   string
	count    atomic.Int32
}

func (t *methodCountingTransport) Connect(ctx context.Context) (mcp.Connection, error) {
	connection, err := t.delegate.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &methodCountingConnection{
		Connection: connection,
		method:     t.method,
		count:      &t.count,
	}, nil
}

type methodCountingConnection struct {
	mcp.Connection
	method string
	count  *atomic.Int32
}

func TestFileURIForAbsolutePath(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{
			name: "Unix",
			path: "/tmp/project root",
			want: "file:///tmp/project%20root",
		},
		{
			name: "Windows",
			path: `C:\Temp\project root`,
			want: "file:///C:/Temp/project%20root",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := fileURIForAbsolutePath(test.path); got != test.want {
				t.Errorf("fileURIForAbsolutePath(%q) = %q, want %q", test.path, got, test.want)
			}
		})
	}
}

func fileURIForAbsolutePath(path string) string {
	slashPath := filepath.ToSlash(path)
	if len(path) >= 3 && path[1] == ':' && (path[2] == '\\' || path[2] == '/') {
		slashPath = "/" + strings.ReplaceAll(path, `\`, "/")
	}
	return (&url.URL{Scheme: "file", Path: slashPath}).String()
}

func (c *methodCountingConnection) Write(ctx context.Context, message jsonrpc.Message) error {
	if request, ok := message.(*jsonrpc.Request); ok && request.Method == c.method {
		c.count.Add(1)
	}
	return c.Connection.Write(ctx, message)
}

func TestDetectSessionRootOnlyListsRootsWhenClientDeclaresCapability(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	declaredRoot := t.TempDir()

	tests := []struct {
		name          string
		capabilities  *mcp.ClientCapabilities
		root          *mcp.Root
		wantListCalls int32
		wantRoot      string
	}{
		{
			name:          "capability absent",
			capabilities:  &mcp.ClientCapabilities{},
			wantListCalls: 0,
			wantRoot:      cwd,
		},
		{
			name: "capability declared with empty roots",
			capabilities: &mcp.ClientCapabilities{
				RootsV2: &mcp.RootCapabilities{},
			},
			wantListCalls: 1,
			wantRoot:      cwd,
		},
		{
			name: "capability declared with a root",
			capabilities: &mcp.ClientCapabilities{
				RootsV2: &mcp.RootCapabilities{},
			},
			root:          &mcp.Root{URI: fileURIForAbsolutePath(declaredRoot)},
			wantListCalls: 1,
			wantRoot:      declaredRoot,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			router, err := store.NewRouterWithDir(t.TempDir())
			if err != nil {
				t.Fatalf("NewRouterWithDir: %v", err)
			}
			t.Cleanup(router.CloseAll)

			srv := NewServer(router)
			// Prevent the initialized notification from racing this explicit
			// detectSessionRoot probe or starting background indexing.
			srv.sessionOnce.Do(func() {})
			srv.updateOnce.Do(func() {})

			clientTransport, rawServerTransport := mcp.NewInMemoryTransports()
			serverTransport := &methodCountingTransport{
				delegate: rawServerTransport,
				method:   "roots/list",
			}
			serverSession, err := srv.MCPServer().Connect(context.Background(), serverTransport, nil)
			if err != nil {
				t.Fatalf("connect server: %v", err)
			}
			t.Cleanup(func() { _ = serverSession.Close() })

			client := mcp.NewClient(
				&mcp.Implementation{Name: "roots-contract-test", Version: "dev"},
				&mcp.ClientOptions{Capabilities: test.capabilities},
			)
			if test.root != nil {
				client.AddRoots(test.root)
			}
			clientSession, err := client.Connect(context.Background(), clientTransport, nil)
			if err != nil {
				t.Fatalf("connect client: %v", err)
			}
			t.Cleanup(func() { _ = clientSession.Close() })

			if got := srv.detectSessionRoot(context.Background(), serverSession); got != test.wantRoot {
				t.Errorf("detectSessionRoot = %q, want %q", got, test.wantRoot)
			}
			if got := serverTransport.count.Load(); got != test.wantListCalls {
				t.Errorf("roots/list calls = %d, want %d", got, test.wantListCalls)
			}
		})
	}
}
