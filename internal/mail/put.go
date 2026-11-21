package mail

import (
	"context"
	"strings"

	"github.com/goccy/go-json"
	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"go.noetive.io/noetive-sdk-go/bud"
)

// PutTool builds the mail_put tool and its handler.
//
// Two kinds of object change, named by the id the way the service names them:
// a message (read, labels, the folder it is filed in), and a mailbox's folder,
// ag_…/<name>, which exists or does not. Any other id is refused here, before
// anything is sent, with the kinds that can be changed.
//
//	tool, handler := mail.PutTool(client)
//	srv.AddTool(tool, handler)
func PutTool(m Mailer) (mcp.Tool, mcpserver.ToolHandlerFunc) {
	tool := mcp.NewTool("mail_put",
		mcp.WithDescription("Change a message: mark it read or unread, replace its labels (the list replaces them all; an empty list removes them), or move it to another folder. "+
			"Make or remove a folder with id ag_…/<name> and changes {\"exists\": true|false}; only an empty folder can be removed. "+
			"precondition_failed on a message means other changes landed first or it is still arriving: repeat it unchanged; on a folder, it still holds mail. Labels replace whole and the last write wins, so read the message again before repeating a label change much later."),
		mcp.WithString("id", mcp.Required(), mcp.Description("The message_ id to change, or ag_…/<folder> for a folder.")),
		mcp.WithObject("changes", mcp.Required(),
			mcp.Description("For a message, at least one of read, labels and folder; for a folder, exists."),
			mcp.Properties(map[string]any{
				"read":   map[string]any{"type": "boolean", "description": "true marks it read, false unread."},
				"labels": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Every label it should carry."},
				"folder": map[string]any{"type": "string", "description": "Move received mail to inbox, quarantine or a folder the mailbox made; sent mail stays in sent."},
				"exists": map[string]any{"type": "boolean", "description": "true makes the folder (lowercase letters, digits, hyphens), false removes it."},
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
			return invalid("mail_put", "%v", err), nil
		}
		id = strings.TrimSpace(id)
		changes, ok := request.GetArguments()["changes"].(map[string]any)
		if !ok || len(changes) == 0 {
			return invalid("mail_put", "changes must be an object: read, labels or folder for a message, exists for a folder"), nil
		}
		key := strings.TrimSpace(request.GetString("idempotency_key", ""))

		switch {
		case strings.HasPrefix(id, bud.PrefixMessage):
			return changeMessage(ctx, m, id, changes, key), nil
		case strings.HasPrefix(id, bud.PrefixAgent) && strings.Contains(id, "/"):
			return changeFolder(ctx, m, id, changes, key), nil
		}
		return invalid("mail_put", "cannot change %s: only a %s id or a folder, %s…/<name>, can be changed; list a mailbox with mail_read to find one", shown(id), bud.PrefixMessage, bud.PrefixAgent), nil
	}

	return tool, handler
}

// changeMessage sends a message's changes as the agent wrote them; the service
// is the one judge of which it accepts.
func changeMessage(ctx context.Context, m Mailer, id string, changes map[string]any, key string) *mcp.CallToolResult {
	raw, err := json.Marshal(changes)
	if err != nil {
		return invalid("mail_put", "changes could not be encoded: %v", err)
	}

	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()

	out, err := m.UpdateMessage(ctx, bud.Change{ID: id, Changes: raw, IdempotencyKey: key})
	switch {
	case err != nil:
		return failed("mail_put", err, "The change may have landed; repeating it as it was lands the same state, unless someone else changed the labels since.")
	case out.Error != nil:
		return refused("mail_put", out.Error)
	}
	return answered(out, "Changed "+id+".")
}

// changeFolder makes or removes the folder ag_…/<name> names. exists is the one
// change a folder takes, so anything beside it is refused rather than dropped.
func changeFolder(ctx context.Context, m Mailer, id string, changes map[string]any, key string) *mcp.CallToolResult {
	mailbox, name, _ := strings.Cut(id, "/")
	exists, ok := changes["exists"].(bool)
	if !ok || len(changes) != 1 || name == "" {
		return invalid("mail_put", "a folder, %s…/<name>, takes only changes {\"exists\": true} to make it or {\"exists\": false} to remove it", bud.PrefixAgent)
	}

	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()

	folder := bud.Folder{In: mailbox, Folder: name, IdempotencyKey: key}
	write, done := m.CreateFolder, "Made "+id+"."
	if !exists {
		write, done = m.DeleteFolder, "Removed "+id+"."
	}
	out, err := write(ctx, folder)
	switch {
	case err != nil:
		return failed("mail_put", err, "The change may have landed; repeating it as it was lands the same state.")
	case out.Error != nil:
		return refused("mail_put", out.Error)
	}
	return answered(out, done)
}
