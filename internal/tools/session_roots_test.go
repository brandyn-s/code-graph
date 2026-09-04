package tools

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/brandyn-s/code-graph/internal/store"
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
		root          *mcp.Root //nolint:staticcheck // MCP roots are deprecated (SEP-2577) but remain functional through the deprecation window; clients still send them
		wantListCalls int32
		wantRoot      string
	}{
		{
			name:          "capability absent",
			capabilities:  &mcp.ClientCapabilities{},
			wantListCalls: 0,
			wantRoot:      cwd,
		},
		// The in-memory SDK client negotiates the latest protocol version
		// (2026-07-28 or newer), at which the specification forbids
		// server-initiated roots/list (SEP-2322 / SEP-2575). The server must
		// therefore not attempt the call even when the capability is declared,
		// and falls back to the working directory. The protocol-version gate
		// itself is covered by TestRootsListingAllowedByProtocolVersion.
		{
			name: "capability declared with empty roots on a new protocol",
			capabilities: &mcp.ClientCapabilities{
				RootsV2: &mcp.RootCapabilities{}, //nolint:staticcheck // MCP roots are deprecated (SEP-2577) but remain functional through the deprecation window; clients still send them
			},
			wantListCalls: 0,
			wantRoot:      cwd,
		},
		{
			name: "capability declared with a root on a new protocol",
			capabilities: &mcp.ClientCapabilities{
				RootsV2: &mcp.RootCapabilities{}, //nolint:staticcheck // MCP roots are deprecated (SEP-2577) but remain functional through the deprecation window; clients still send them
			},
			root:          &mcp.Root{URI: fileURIForAbsolutePath(declaredRoot)}, //nolint:staticcheck // MCP roots are deprecated (SEP-2577) but remain functional through the deprecation window; clients still send them
			wantListCalls: 0,
			wantRoot:      cwd,
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
				client.AddRoots(test.root) //nolint:staticcheck // MCP roots are deprecated (SEP-2577) but remain functional through the deprecation window; clients still send them
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

func TestRootsListingAllowedByProtocolVersion(t *testing.T) {
	withRoots := &mcp.ClientCapabilities{
		RootsV2: &mcp.RootCapabilities{}, //nolint:staticcheck // MCP roots are deprecated (SEP-2577) but remain functional through the deprecation window; clients still send them
	}
	tests := []struct {
		name   string
		params *mcp.InitializeParams
		want   bool
	}{
		{name: "nil params", params: nil, want: false},
		{name: "no capabilities", params: &mcp.InitializeParams{ProtocolVersion: "2025-11-25"}, want: false},
		{name: "roots declared on 2025-11-25", params: &mcp.InitializeParams{ProtocolVersion: "2025-11-25", Capabilities: withRoots}, want: true},
		{name: "roots declared on 2025-06-18", params: &mcp.InitializeParams{ProtocolVersion: "2025-06-18", Capabilities: withRoots}, want: true},
		{name: "roots absent on 2025-11-25", params: &mcp.InitializeParams{ProtocolVersion: "2025-11-25", Capabilities: &mcp.ClientCapabilities{}}, want: false},
		{name: "roots declared on 2026-07-28", params: &mcp.InitializeParams{ProtocolVersion: "2026-07-28", Capabilities: withRoots}, want: false},
		{name: "roots declared on a later version", params: &mcp.InitializeParams{ProtocolVersion: "2027-01-01", Capabilities: withRoots}, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := rootsListingAllowed(test.params); got != test.want {
				t.Errorf("rootsListingAllowed = %v, want %v", got, test.want)
			}
		})
	}
}
