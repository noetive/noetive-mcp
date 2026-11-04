package mail

import (
	"context"
	"fmt"
	"strings"

	"github.com/goccy/go-json"
	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"go.noetive.io/noetive-sdk-go/bud"
)

// SendTool builds the bud_send tool and its handler.
//
// The only call here that reaches outside the workspace, and the one whose
// mistake cannot be taken back: a second copy of a message. So the
// idempotency key is required by the schema, and a held result says in words
// that sending again is that mistake.
//
// Annotated not idempotent, although a repeat under the same key sends nothing:
// every fresh key is a new real email, and a client that auto-approves calls it
// is told are idempotent should not approve this one.
//
//	tool, handler := mail.SendTool(client)
//	srv.AddTool(tool, handler)
func SendTool(m Mailer) (mcp.Tool, mcpserver.ToolHandlerFunc) {
	addresses := func(name, what string) mcp.ToolOption {
		return mcp.WithArray(name, mcp.WithStringItems(), mcp.Description(what))
	}

	tool := mcp.NewTool("bud_send",
		mcp.WithDescription("Send a message, or reply with in_reply_to. Subject to your mailbox's limits and its workspace's policy. "+
			"Give each message a fresh idempotency_key and reuse it only to retry that same message: it then goes out once. "+
			"A held result is waiting for a person to approve it. It is not an error, and sending again sends a second copy."),
		addresses("to", "Recipient addresses. May be left out on a reply, which takes them from the message replied to."),
		addresses("cc", "Carbon-copy addresses."),
		addresses("bcc", "Blind-copy addresses."),
		mcp.WithString("subject", mcp.Description("Subject line. A reply takes the original's when left out.")),
		mcp.WithString("text", mcp.Description("Plain-text body.")),
		mcp.WithString("html", mcp.Description("HTML body, beside or instead of text.")),
		mcp.WithString("in_reply_to", mcp.Description("The message_ id this answers. Threads the reply and fills its recipients.")),
		mcp.WithBoolean("reply_all", mcp.Description("With in_reply_to: answer every recipient of the original, not only its sender.")),
		mcp.WithString("from", mcp.Description("Which of your addresses to send as. Empty sends as your primary address.")),
		mcp.WithString("on_behalf_of", mcp.Description("Send as another agent, by its ag_ id. Needs a send-on-behalf grant; refused as policy_refused without one.")),
		mcp.WithObject("agent", mcp.Description("A machine-readable part for an agent recipient: a JSON object sent beside the text.")),
		mcp.WithString("idempotency_key", mcp.Required(),
			mcp.Description("A key unique to this message. A repeat with the same key returns the first result and sends nothing; a different message under a used key is not sent.")),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithOpenWorldHintAnnotation(true),
	)

	handler := func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		key, err := request.RequireString("idempotency_key")
		if err != nil || strings.TrimSpace(key) == "" {
			return invalid("bud_send", "idempotency_key is required: a send without one that times out and is retried goes out twice"), nil
		}

		var recipients [3][]string
		for i, name := range []string{"to", "cc", "bcc"} {
			list, err := addressesOf(request.GetArguments()[name])
			if err != nil {
				return invalid("bud_send", "%s: %v", name, err), nil
			}
			recipients[i] = list
		}

		in := bud.SendInput{
			To:             recipients[0],
			CC:             recipients[1],
			BCC:            recipients[2],
			Subject:        request.GetString("subject", ""),
			Text:           request.GetString("text", ""),
			HTML:           request.GetString("html", ""),
			InReplyTo:      strings.TrimSpace(request.GetString("in_reply_to", "")),
			ReplyAll:       request.GetBool("reply_all", false),
			From:           strings.TrimSpace(request.GetString("from", "")),
			OnBehalfOf:     strings.TrimSpace(request.GetString("on_behalf_of", "")),
			IdempotencyKey: strings.TrimSpace(key),
		}
		if agent, ok := request.GetArguments()["agent"]; ok && agent != nil {
			part, ok := agent.(map[string]any)
			if !ok {
				return invalid("bud_send", "agent must be a JSON object"), nil
			}
			raw, err := json.Marshal(part)
			if err != nil {
				return invalid("bud_send", "agent could not be encoded: %v", err), nil
			}
			in.Agent = raw
		}

		ctx, cancel := context.WithTimeout(ctx, callTimeout)
		defer cancel()

		out, err := m.Send(ctx, in)
		switch {
		case err != nil:
			return failed("bud_send", err, "It may have been sent. Retry with the same idempotency_key, which cannot send it twice; "+
				"if that is refused as rate_limited, the first attempt is still running, so wait retry_after and repeat with the same key, never a new one."), nil
		case out.Error != nil:
			return refused("bud_send", out.Error), nil
		}
		return answered(out, sendText(out)), nil
	}

	return tool, handler
}

// sendText says what happened to the message, and for a held one what not to
// do about it.
func sendText(out bud.SendOutput) string {
	switch out.State {
	case bud.StateHeld:
		approval := "an approval"
		if out.ApprovalID != "" {
			approval = "approval " + out.ApprovalID
		}
		return fmt.Sprintf("Held: message %s waits on %s. A person decides whether it goes; nothing you send releases it. Do not send it again: that would be a second message.", out.Message, approval)
	case bud.StateQueued:
		text := fmt.Sprintf("Queued: message %s in thread %s is accepted for delivery.", out.Message, out.Thread)
		if out.Reason != "" {
			// "provisioning" or "paused": accepted, but not going out yet, which
			// the user should hear rather than assume it went.
			text += fmt.Sprintf(" It waits before going out: %q.", out.Reason)
		}
		return text
	default:
		// A repeat with a used key reports the first message's state now.
		text := fmt.Sprintf("This key was already used: message %s, sent under it before, is now %s. Nothing new was sent; a different message needs a new key.", out.Message, out.State)
		if out.Reason != "" {
			text += fmt.Sprintf(" Reason: %q.", out.Reason)
		}
		return text
	}
}

// addressesOf reads a recipient list, refusing anything that is not a list of
// strings: dropping a recipient an agent named would send to fewer people than
// it believes.
func addressesOf(v any) ([]string, error) {
	if v == nil {
		return nil, nil
	}
	items, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("must be a list of addresses")
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		address, ok := item.(string)
		if !ok || strings.TrimSpace(address) == "" {
			return nil, fmt.Errorf("every entry must be an address, got %v", item)
		}
		out = append(out, strings.TrimSpace(address))
	}
	return out, nil
}
