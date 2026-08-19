package broker

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/noetive/noetive-sdk-go/semantik"

	"github.com/noetive/noetive-mcp/internal/targeting"
)

// Publisher appends a message to a namespace.
type Publisher interface {
	Publish(ctx context.Context, req semantik.PublishRequest) (semantik.PublishResponse, error)
}

// publication is the structured result of a publish, returned to clients that
// negotiated a revision supporting structured content. The identifiers let an
// agent correlate what it wrote with what it later reads back.
//
// Field ordering: string (16 B) > uint64s (8 B each).
type publication struct {
	MessageID string `json:"message_id"`
	Epoch     uint64 `json:"epoch"`
	Seq       uint64 `json:"seq"`
}

// PublishTool builds the noetive_publish tool and its handler. fallback
// supplies routing fields the operator configured; anything it leaves unset
// must arrive on the call.
//
//	tool, handler := broker.PublishTool(client, configured)
//	srv.AddTool(tool, handler)
func PublishTool(p Publisher, fallback targeting.Target) (mcp.Tool, mcpserver.ToolHandlerFunc) {
	options := []mcp.ToolOption{
		mcp.WithDescription("Publish a message to a Noetive Semantik namespace so other agents and subscribers can find it by meaning. Returns the server-assigned message id and its position in the log."),
		mcp.WithString("text",
			mcp.Required(),
			mcp.Description("The message to publish. The server embeds it, so write what you would want a peer searching by meaning to find."),
		),
		mcp.WithObject("metadata",
			mcp.Description("Flat string-to-string labels stored with the message and returned on every search hit, for example {\"topic\":\"payments\"}."),
			mcp.AdditionalProperties(map[string]any{"type": "string"}),
		),
		mcp.WithString("idempotency_key",
			mcp.Description("Caller-chosen key that makes a retry safe: republishing with the same key returns the original message instead of a duplicate."),
		),
		mcp.WithString("ack",
			mcp.Description("Durability to wait for. \"stored\" returns once the message is accepted; \"durable\" waits for replication. Defaults to \"stored\"."),
			mcp.Enum(string(semantik.AckStored), string(semantik.AckDurable)),
		),
	}
	options = append(options, targetingOptions()...)

	tool := mcp.NewTool("noetive_publish", options...)

	handler := func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		text, err := request.RequireString("text")
		if err != nil {
			return mcp.NewToolResultErrorFromErr("noetive_publish: invalid arguments", err), nil
		}
		if strings.TrimSpace(text) == "" {
			return mcp.NewToolResultError("noetive_publish: text must not be empty"), nil
		}

		requested, err := requestedTarget(request)
		if err != nil {
			return mcp.NewToolResultErrorFromErr("noetive_publish: invalid arguments", err), nil
		}
		target, err := targeting.Resolve(requested, fallback)
		if err != nil {
			return mcp.NewToolResultErrorFromErr("noetive_publish", err), nil
		}

		metadata, err := stringMap(request.GetArguments()["metadata"])
		if err != nil {
			return mcp.NewToolResultErrorFromErr("noetive_publish: invalid metadata", err), nil
		}

		ack := semantik.AckMode(request.GetString("ack", ""))
		if ack != "" && !ack.Valid() {
			return mcp.NewToolResultErrorf("noetive_publish: ack must be %q or %q, got %q", semantik.AckStored, semantik.AckDurable, ack), nil
		}

		ctx, cancel := context.WithTimeout(ctx, queryTimeout)
		defer cancel()

		resp, err := p.Publish(ctx, semantik.PublishRequest{
			Metadata:       metadata,
			Items:          []semantik.PublishItem{{Text: text}},
			Namespace:      target.Namespace,
			Model:          target.Model,
			IdempotencyKey: request.GetString("idempotency_key", ""),
			Ack:            ack,
			Dimensions:     target.Dimensions,
		})
		if err != nil {
			return failure("noetive_publish", err), nil
		}

		published := publication{MessageID: resp.MessageID, Epoch: resp.Epoch, Seq: resp.Seq}
		return mcp.NewToolResultStructured(published, describePublication(target.Namespace, resp)), nil
	}

	return tool, handler
}

// describePublication renders the text fallback, naming where the message
// landed and the ordering tokens that let an agent correlate it later.
//
// Built with direct writes rather than Sprintf: publish runs on every message
// an agent shares, and the formatter's reflection over four arguments is the
// single largest cost in an otherwise trivial function.
func describePublication(namespace string, resp semantik.PublishResponse) string {
	var b strings.Builder
	b.Grow(len(namespace) + len(resp.MessageID) + 48)

	b.WriteString("Published to ")
	b.WriteString(namespace)
	b.WriteString(" as ")
	b.WriteString(resp.MessageID)
	b.WriteString(" (epoch ")
	b.WriteString(strconv.FormatUint(resp.Epoch, 10))
	b.WriteString(", seq ")
	b.WriteString(strconv.FormatUint(resp.Seq, 10))
	b.WriteString(").")

	return b.String()
}

// stringMap converts a decoded JSON object into the flat string map the wire
// accepts. A non-string value is refused rather than stringified, because
// coercing 42 to "42" would make the stored label disagree with what the agent
// believes it wrote.
func stringMap(raw any) (map[string]string, error) {
	if raw == nil {
		return map[string]string{}, nil
	}

	object, ok := raw.(map[string]any)
	if !ok {
		return map[string]string{}, fmt.Errorf("expected an object, got %T", raw)
	}

	out := make(map[string]string, len(object))
	var rejected []string
	for k, v := range object {
		s, ok := v.(string)
		if !ok {
			rejected = append(rejected, fmt.Sprintf("%s (%T)", k, v))
			continue
		}
		out[k] = s
	}
	if len(rejected) > 0 {
		sort.Strings(rejected)
		return map[string]string{}, fmt.Errorf("every value must be a string; these are not: %s", strings.Join(rejected, ", "))
	}

	return out, nil
}
