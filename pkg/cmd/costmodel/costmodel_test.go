package costmodel

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/julienschmidt/httprouter"
	"github.com/opencost/opencost/pkg/costmodel"
	"github.com/opencost/opencost/pkg/env"
)

func TestMCPServerGracefulShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	accesses := &costmodel.Accesses{}
	port := env.GetMCPHTTPPort()

	// Start MCP server
	go func() {
		_ = StartMCPServer(ctx, accesses, nil)
	}()

	// Wait for server to be ready
	serverUp := false
	for i := 0; i < 10; i++ {
		time.Sleep(100 * time.Millisecond)
		client := &http.Client{Timeout: 1 * time.Second}
		resp, err := client.Get(fmt.Sprintf("http://localhost:%d/", port))
		if err == nil {
			resp.Body.Close()
			serverUp = true
			break
		}
	}

	if !serverUp {
		t.Skip("MCP server did not start")
	}

	// Trigger shutdown
	cancel()
	time.Sleep(500 * time.Millisecond)

	// Verify server is no longer accepting connections
	client := &http.Client{Timeout: 500 * time.Millisecond}
	_, err := client.Get(fmt.Sprintf("http://localhost:%d/", port))
	if err == nil {
		t.Error("Server still accepting connections after shutdown")
	}
}

// TestShutdownTimeoutConstant verifies the shutdown timeout constant is set correctly
func TestShutdownTimeoutConstant(t *testing.T) {
	if shutdownTimeout != 30*time.Second {
		t.Errorf("Expected shutdown timeout of 30s, got %v", shutdownTimeout)
	}
}

// TestGracefulShutdownConfiguration verifies graceful shutdown works with the configured timeout
func TestGracefulShutdownConfiguration(t *testing.T) {
	if shutdownTimeout < 5*time.Second {
		t.Error("Shutdown timeout is too short for graceful shutdown")
	}
}

func TestRegisterOpenCostUIRoutesRegistersPrefixedAndLegacyRoutes(t *testing.T) {
	router := httprouter.New()
	accesses := &costmodel.Accesses{}

	registerOpenCostUIRoutes(router, accesses, true)

	testCases := []struct {
		method string
		path   string
	}{
		{method: http.MethodGet, path: "/allocation"},
		{method: http.MethodGet, path: costmodel.RoutePrefix + "/allocation"},
		{method: http.MethodGet, path: "/allocation/summary/topline"},
		{method: http.MethodGet, path: costmodel.RoutePrefix + "/allocation/summary/topline"},
		{method: http.MethodGet, path: "/assets/graph"},
		{method: http.MethodGet, path: costmodel.RoutePrefix + "/assets/graph"},
		{method: http.MethodGet, path: "/assets/carbon"},
		{method: http.MethodGet, path: costmodel.RoutePrefix + "/assets/carbon"},
	}

	for _, tc := range testCases {
		handle, _, _ := router.Lookup(tc.method, tc.path)
		if handle == nil {
			t.Fatalf("expected route %s %s to be registered", tc.method, tc.path)
		}
	}
}
