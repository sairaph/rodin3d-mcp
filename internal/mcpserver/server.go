// Package mcpserver is the MCP surface of rodin3d-mcp: one tool per
// operation, each a thin wrapper over the service method of the same name.
package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sairaph/rodin3d-mcp/internal/domain"
	"github.com/sairaph/rodin3d-mcp/internal/service"
)

// Config holds the MCP server configuration.
type Config struct {
	Version   string
	Transport string // "stdio" or "http"
	HTTPAddr  string // listen address for HTTP mode
	Service   *service.Service
}

// Server wraps the MCP server and the service its tools call.
type Server struct {
	mcpServer *mcp.Server
	config    Config
	svc       *service.Service
}

const instructions = "Rodin (Hyper3D) image-to-3D generations of the signed-in account. " +
	"Start with get_account; when it answers authentication, call login and give the user the link it returns. " +
	"list_tasks and get_task read the generations with every setting, get_preview returns their images, " +
	"download_model saves packed model files, and quote_cost gives the price of an operation before anything is spent."

// New creates the server and registers its tools.
func New(config Config) *Server {
	srv := &Server{
		config: config,
		svc:    config.Service,
		mcpServer: mcp.NewServer(
			&mcp.Implementation{Name: domain.ServerName, Title: "Rodin 3D (Hyper3D)", Version: config.Version},
			&mcp.ServerOptions{
				Instructions: instructions,
				Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{}},
			},
		),
	}
	srv.registerTools()
	srv.mcpServer.AddReceivingMiddleware(invalidArguments)
	return srv
}

// MCPServer exposes the underlying server, for tests.
func (s *Server) MCPServer() *mcp.Server { return s.mcpServer }

// Run starts the server and blocks until ctx is cancelled.
func (s *Server) Run(ctx context.Context) error {
	s.svc.EnsureDaemon(ctx)
	switch s.config.Transport {
	case "http":
		return s.runHTTP(ctx)
	default:
		return s.mcpServer.Run(ctx, &mcp.StdioTransport{})
	}
}

func (s *Server) runHTTP(ctx context.Context) error {
	addr := s.config.HTTPAddr
	if addr == "" {
		addr = "127.0.0.1:8080"
	}
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s.mcpServer }, &mcp.StreamableHTTPOptions{})
	httpServer := &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
		case <-done:
			return
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			fmt.Fprintf(os.Stderr, "error shutting down HTTP server: %v\n", err)
		}
	}()
	fmt.Fprintf(os.Stderr, "%s listening on %s (Streamable HTTP)\n", domain.BinaryName, addr)
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve http: %w", err)
	}
	return nil
}
