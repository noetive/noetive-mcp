package mail

import (
	"context"
	"strings"

	"github.com/goccy/go-json"
	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"go.noetive.io/noetive-sdk-go/bud"
)

// PutTool builds the bud_put tool and its handler.
//
// A message is the one object an agent changes: read or unread, and its
// labels. Any other id is refused here, before anything is sent, with the kind
// that can be changed.
//
//	tool, handler := mail.PutTool(client)
//	srv.AddTool(tool, handler)
func PutTool(m Mailer) (mcp.Tool, mcpserver.ToolHandlerFunc) {
	tool := mcp.NewTool("bud_put",
		mcp.WithDescription("Change a message: mark it read or unread, or replace its labels (the list replaces them all; an empty list removes them). "+
			"Safe to repeat as it was. precondition_failed means other changes landed first: repeat it unchanged."),
		mcp.WithString("id", mcp.Required(), mcp.Description("The message_ id to change.")),
		mcp.WithObject("changes", mcp.Required(),
			mcp.Description("At least one of read and labels."),
			mcp.Properties(map[string]any{
				"read":   map[string]any{"type": "boolean", "description": "true marks it read, false unread."},
				"labels": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Every label it should carry."},
			}),
		),
		mcp.WithString("idempotency_key", mcp.Description("Optional. The same key repeats this change instead of applying it twice; never reuse one for a different change.")),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithOpenWorldHintAnnotation(false),
	)

	handler := func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := request.RequireString("id")
		if err != nil {
			return invalid("bud_put", "%v", err), nil
		}
		id = strings.TrimSpace(id)
		if !strings.HasPrefix(id, bud.PrefixMessage) {
			return invalid("bud_put", "cannot change %s: only a %s id can be changed", shown(id), bud.PrefixMessage), nil
		}

		changes, ok := request.GetArguments()["changes"].(map[string]any)
		if !ok || len(changes) == 0 {
			return invalid("bud_put", "changes must be an object with read, labels or both"), nil
		}
		raw, err := json.Marshal(changes)
		if err != nil {
			return invalid("bud_put", "changes could not be encoded: %v", err), nil
		}

		ctx, cancel := context.WithTimeout(ctx, callTimeout)
		defer cancel()

		out, err := m.UpdateMessage(ctx, bud.Change{
			ID:             id,
			Changes:        raw,
			IdempotencyKey: strings.TrimSpace(request.GetString("idempotency_key", "")),
		})
		switch {
		case err != nil:
			return failed("bud_put", err, "The change may have landed; repeating it as it was is safe."), nil
		case out.Error != nil:
			return refused("bud_put", out.Error), nil
		}
		return answered(out, "Changed "+id+"."), nil
	}

	return tool, handler
}
