package mail_test

import (
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"go.noetive.io/noetive-sdk-go/bud"

	"go.noetive.io/noetive-mcp/internal/mail"
)

// FuzzReadRoutesOnlyByAPrefixItNames covers bud_read's routing over any id an
// agent might send, including ones copied out of a message: it never panics, and
// it reaches an operation only for an id whose prefix names that operation's
// kind. Anything else is refused before a request is sent.
func FuzzReadRoutesOnlyByAPrefixItNames(f *testing.F) {
	for _, seed := range []string{"me", "catalog", "ag_01x", "message_01x", "thread_01x", "", "ag", "journal_01x", "Me", " me ", "\x00", strings.Repeat("ag_", 100)} {
		f.Add(seed, "")
		f.Add(seed, "2")
	}

	f.Fuzz(func(t *testing.T, id, part string) {
		r := &recorder{}
		_, handler := mail.ReadTool(r)
		var request mcp.CallToolRequest
		request.Params.Arguments = map[string]any{"id": id, "part": part}
		result, err := handler(t.Context(), request)
		if err != nil || result == nil {
			t.Fatalf("id %q: result %v, error %v", id, result, err)
		}

		trimmed := strings.TrimSpace(id)
		want := ""
		switch {
		case trimmed == "me":
			want = "DescribeMe"
		case trimmed == "catalog":
			want = "DescribeCatalog"
		case strings.HasPrefix(trimmed, bud.PrefixAgent):
			want = "DescribeMailbox"
		case strings.HasPrefix(trimmed, bud.PrefixMessage) && part != "":
			want = "DescribePart"
		case strings.HasPrefix(trimmed, bud.PrefixMessage):
			want = "DescribeMessage"
		case strings.HasPrefix(trimmed, bud.PrefixThread):
			want = "DescribeThread"
		}
		if r.called != want {
			t.Fatalf("id %q part %q reached %q, want %q", id, part, r.called, want)
		}
		if want == "" && !result.IsError {
			t.Fatalf("id %q reached nothing and was not refused", id)
		}
	})
}
