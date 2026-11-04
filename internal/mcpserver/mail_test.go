package mcpserver_test

import (
	"slices"
	"strings"
	"testing"

	"go.noetive.io/noetive-mcp/internal/mcpserver"
	"go.noetive.io/noetive-mcp/internal/targeting"
)

// stubMailer stands in for a Bud client where only the tool listing is under
// test. Its methods are the nil interface's, so a test that calls one fails
// loudly rather than reading a zero value as an answer.
type stubMailer struct{ mcpserver.Mailer }

// The mail tools take the same key as everything else, so a server without one
// must not offer them: each could only refuse, and still costs its description
// on every turn. The instructions have to say they are missing too, or an agent
// asked to read mail reports the server broken.
func TestAnUnconfiguredServerOffersNoMailTools(t *testing.T) {
	srv := mcpserver.New("test", mcpserver.Unconfigured("no key"), targeting.Policy{}, mcpserver.WithBud(stubMailer{}))

	for _, name := range toolNamesOf(t, srv) {
		if strings.HasPrefix(name, "bud_") {
			t.Errorf("an unconfigured server registers %s", name)
		}
	}
	if got := instructionsOf(t, srv); !strings.Contains(got, "bud_read") {
		t.Errorf("the instructions do not say the mail tools are absent: %s", got)
	}
}

// A keyed server with Bud registers exactly the four mail verbs and tells the
// agent where to start; one without Bud registers none of them, so ToolNames
// describes the server main builds rather than every one New can build.
func TestAKeyedServerOffersTheMailToolsItWasGiven(t *testing.T) {
	with := mcpserver.New("test", &stubBroker{}, targeting.Policy{}, mcpserver.WithBud(stubMailer{}))
	var mail []string
	for _, name := range toolNamesOf(t, with) {
		if strings.HasPrefix(name, "bud_") {
			mail = append(mail, name)
		}
	}
	slices.Sort(mail)
	if want := []string{"bud_put", "bud_read", "bud_send", "bud_wait"}; !slices.Equal(mail, want) {
		t.Errorf("mail tools %v, want %v", mail, want)
	}
	if got := instructionsOf(t, with); !strings.Contains(got, "bud_read id=me") {
		t.Errorf("the instructions do not say where to start: %s", got)
	}

	without := mcpserver.New("test", &stubBroker{}, targeting.Policy{})
	for _, name := range toolNamesOf(t, without) {
		if strings.HasPrefix(name, "bud_") {
			t.Errorf("a server given no mailer registers %s", name)
		}
	}
}
