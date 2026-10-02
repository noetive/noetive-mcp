package mcpserver_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	json "github.com/goccy/go-json"
	"github.com/mark3labs/mcp-go/mcp"
	"go.noetive.io/noetive-sdk-go/semantik"

	"go.noetive.io/noetive-mcp/internal/mcpserver"
	"go.noetive.io/noetive-mcp/internal/targeting"
)

// Without a credential the tools that spend quota must not be offered: each one
// could only fail, and still costs its description in every turn. Health is the
// exception, because it is the one tool that can say what is wrong.
func TestUnconfiguredServerOffersOnlyHealth(t *testing.T) {
	srv := mcpserver.New("test", mcpserver.Unconfigured("NOETIVE_KEY_SECRET is not set"), targeting.Policy{})

	got := toolNamesOf(t, srv)
	if len(got) != 1 || got[0] != "noetive_health" {
		t.Fatalf("expected only noetive_health, got %v", got)
	}
}

// The agent reads the instructions first. They have to carry the warning, or it
// goes looking for tools that are not there and reports the server broken.
func TestUnconfiguredServerWarnsInItsInstructions(t *testing.T) {
	srv := mcpserver.New("test", mcpserver.Unconfigured("NOETIVE_KEY_SECRET is not set"), targeting.Policy{})

	got := instructionsOf(t, srv)
	if !strings.Contains(got, "WARNING") || !strings.Contains(got, "NOETIVE_KEY_SECRET is not set") {
		t.Errorf("expected a warning that names the cause, got: %s", got)
	}
}

// The refusal has to name the missing variable. A generic "unauthorized" would
// send the user looking at their account rather than their environment.
func TestUnconfiguredCallsExplainWhatIsMissing(t *testing.T) {
	srv := mcpserver.New("test", mcpserver.Unconfigured("NOETIVE_KEY_SECRET is not set for this editor"), targeting.Policy{})

	srv.HandleMessage(context.Background(), json.RawMessage(initialize))
	srv.HandleMessage(context.Background(), json.RawMessage(initialized))
	resp := srv.HandleMessage(context.Background(),
		json.RawMessage(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"noetive_health","arguments":{}}}`))

	var envelope struct {
		Result mcp.CallToolResult `json:"result"`
	}
	decode(t, resp, &envelope)

	if !envelope.Result.IsError {
		t.Fatal("expected an error result from an unconfigured server")
	}

	var message strings.Builder
	for _, c := range envelope.Result.Content {
		if tc, ok := c.(mcp.TextContent); ok {
			message.WriteString(tc.Text)
		}
	}

	if !strings.Contains(message.String(), "NOETIVE_KEY_SECRET") {
		t.Errorf("expected the missing variable to be named, got: %s", message.String())
	}
}

// Every operation must refuse, not just the one a test happens to call. A
// method that slipped through would reach the broker with no credential and
// return a server-side error the user cannot act on.
func TestEveryOperationIsRefusedWhenUnconfigured(t *testing.T) {
	const reason = "NOETIVE_KEY_SECRET is not set for this editor"
	b := mcpserver.Unconfigured(reason)
	ctx := context.Background()

	operations := map[string]func() error{
		"publish": func() error {
			_, err := b.Publish(ctx, semantik.PublishRequest{})
			return err
		},
		"search": func() error {
			_, err := b.Search(ctx, semantik.SearchRequest{})
			return err
		},
		"subscribe": func() error {
			_, err := b.Subscribe(ctx, semantik.SubscribeRequest{})
			return err
		},
		"lint": func() error {
			_, err := b.Lint(ctx, semantik.LintRequest{})
			return err
		},
		"health": func() error { return b.Health(ctx) },
	}

	for name, call := range operations {
		t.Run(name, func(t *testing.T) {
			err := call()
			if err == nil {
				t.Fatal("expected the call to be refused")
			}
			if !strings.Contains(err.Error(), reason) {
				t.Errorf("expected the reason to survive, got: %v", err)
			}
		})
	}
}

// The refusal is shaped as a pre-flight error, HTTPStatus zero, so it travels
// the same path as any other rejection that never reached the wire, and the
// agent is told the request was not sent rather than that the service failed.
func TestRefusalIsReportedAsPreflightNotAsAServerFailure(t *testing.T) {
	err := mcpserver.Unconfigured("no key").Health(context.Background())

	var apiErr *semantik.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected a *semantik.Error, got %T", err)
	}
	if apiErr.HTTPStatus != 0 {
		t.Errorf("expected HTTPStatus 0 to mark this as pre-flight, got %d", apiErr.HTTPStatus)
	}
	if apiErr.Code != semantik.CodeUnauthorized {
		t.Errorf("expected an unauthorized code, got %q", apiErr.Code)
	}
}
