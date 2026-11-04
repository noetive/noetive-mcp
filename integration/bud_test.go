//go:build integration

package integration

import (
	"os"
	"strings"
	"testing"

	json "github.com/goccy/go-json"
	"go.noetive.io/noetive-sdk-go/bud"
	"go.noetive.io/noetive-sdk-go/semantik"

	"go.noetive.io/noetive-mcp/internal/mcpserver"
)

// newMailSession is a session on the assembled server with the Bud tools, on
// the same key as everything else.
func newMailSession(t *testing.T) *session {
	t.Helper()

	if strings.TrimSpace(os.Getenv("NOETIVE_KEY_SECRET")) == "" {
		t.Skip("NOETIVE_KEY_SECRET is not set; skipping integration tests")
	}
	client, err := semantik.NewFromEnv()
	if err != nil {
		t.Fatalf("could not build the Semantik client: %v", err)
	}
	mailer, err := bud.NewFromEnv()
	if err != nil {
		t.Fatalf("could not build the Bud client: %v", err)
	}
	return open(t, mcpserver.New("integration", client, shared, mcpserver.WithBud(mailer)))
}

// TestTheMailToolsReadTheLiveMailbox drives bud_read through the assembled
// server against the live service: me names a mailbox, and that mailbox reads.
// It catches the tool and the service disagreeing about a route or a field,
// which no stub can.
func TestTheMailToolsReadTheLiveMailbox(t *testing.T) {
	s := newMailSession(t)

	me := s.call("bud_read", map[string]any{"id": "me"})
	if me.IsError {
		t.Fatalf("bud_read me refused: %s", s.text(me))
	}
	var who struct {
		Content struct {
			Agent     string `json:"agent"`
			Workspace string `json:"workspace"`
			Mailbox   string `json:"mailbox"`
		} `json:"content"`
	}
	decodeStructured(t, me.StructuredContent, &who)
	if !strings.HasPrefix(who.Content.Agent, bud.PrefixAgent) || !strings.HasPrefix(who.Content.Workspace, bud.PrefixWorkspace) {
		t.Fatalf("me names agent %q in workspace %q", who.Content.Agent, who.Content.Workspace)
	}

	mailbox := who.Content.Mailbox
	if mailbox == "" {
		mailbox = who.Content.Agent
	}
	described := s.call("bud_read", map[string]any{"id": mailbox})
	if described.IsError {
		t.Fatalf("bud_read %s refused: %s", mailbox, s.text(described))
	}
	if !strings.Contains(s.text(described), mailbox) && described.StructuredContent == nil {
		t.Errorf("bud_read %s returned neither its id nor structured content", mailbox)
	}
}

// TestAnUnknownIdIsRefusedWithoutAskingTheService: the refusal is the tool's,
// so it arrives even when the service would have spent a request on it.
func TestAnUnknownIdIsRefusedWithoutAskingTheService(t *testing.T) {
	s := newMailSession(t)

	result := s.call("bud_read", map[string]any{"id": "journal_01x"})
	if !result.IsError || !strings.Contains(s.text(result), "bud_read id=me") {
		t.Errorf("an unknown id came back as %s", s.text(result))
	}
}

// decodeStructured reads a tool result's structured content into target.
func decodeStructured(t *testing.T, structured any, target any) {
	t.Helper()
	raw, err := json.Marshal(structured)
	if err != nil {
		t.Fatalf("could not encode structured content: %v", err)
	}
	if err := json.Unmarshal(raw, target); err != nil {
		t.Fatalf("could not decode %s: %v", raw, err)
	}
}
