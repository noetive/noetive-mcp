package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/noetive/noetive-mcp/internal/server"
)

func callHelloWorld(t *testing.T, healthURL string) *mcp.CallToolResult {
	t.Helper()
	s := server.New("test", healthURL)
	ctx := context.Background()

	// Initialize
	s.HandleMessage(ctx, json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}}`))
	s.HandleMessage(ctx, json.RawMessage(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))

	// Call the tool
	resp := s.HandleMessage(ctx, json.RawMessage(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"hello_world","arguments":{}}}`))

	return extractToolResult(t, resp)
}

func extractToolResult(t *testing.T, msg mcp.JSONRPCMessage) *mcp.CallToolResult {
	t.Helper()

	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("failed to marshal response: %v", err)
	}
	var envelope struct {
		Result mcp.CallToolResult `json:"result"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatalf("failed to unmarshal response %s: %v", string(data), err)
	}
	return &envelope.Result
}

func TestHealthCheckSuccess(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"healthy"}`))
	}))
	defer ts.Close()

	result := callHelloWorld(t, ts.URL)

	if result.IsError {
		t.Fatal("expected success, got error")
	}
	if len(result.Content) != 1 {
		t.Fatalf("expected exactly 1 content item, got %d", len(result.Content))
	}
	text, ok := result.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("expected TextContent, got %T", result.Content[0])
	}
	if text.Text != `{"status":"healthy"}` {
		t.Errorf("expected health response body, got %q", text.Text)
	}
}

func TestHealthCheckServerError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal error"))
	}))
	defer ts.Close()

	result := callHelloWorld(t, ts.URL)

	if !result.IsError {
		t.Fatal("expected error result for 500 status")
	}
	if len(result.Content) != 1 {
		t.Fatalf("expected exactly 1 content item, got %d", len(result.Content))
	}
}

func TestHealthCheckUnreachable(t *testing.T) {
	result := callHelloWorld(t, "http://127.0.0.1:1")

	if !result.IsError {
		t.Fatal("expected error result for unreachable server")
	}
	if len(result.Content) != 1 {
		t.Fatalf("expected exactly 1 content item, got %d", len(result.Content))
	}
}

func TestToolAlwaysReturnsSingleContentItem(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))
	defer ts.Close()

	scenarios := []struct {
		name string
		url  string
	}{
		{"success", ts.URL},
		{"unreachable", "http://127.0.0.1:1"},
	}

	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			result := callHelloWorld(t, sc.url)
			if len(result.Content) != 1 {
				t.Errorf("expected exactly 1 content item, got %d", len(result.Content))
			}
		})
	}
}
