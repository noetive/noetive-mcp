package broker

import (
	"context"
	"strconv"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/noetive/noetive-sdk-go/semantik"
)

// Linter parses a SemQL query and reports diagnostics and completions.
type Linter interface {
	Lint(ctx context.Context, req semantik.LintRequest) (semantik.LintResponse, error)
}

// linted is the structured result of a lint. Diagnostics and completions are
// passed through as the server phrased them; rewording them here would put a
// second, staler explanation of SemQL in front of the agent.
//
// Field ordering: string (one pointer at offset 0) > slices > bool, which
// packs the pointer words ahead of the rest.
type linted struct {
	Normalized  string                    `json:"normalized,omitempty"`
	Diagnostics []semantik.LintDiagnostic `json:"diagnostics,omitempty"`
	Completions []semantik.LintCompletion `json:"completions,omitempty"`
	Valid       bool                      `json:"valid"`
}

// LintTool builds the noetive_lint tool and its handler.
//
// Lint carries no routing triple: the server parses SemQL without touching a
// namespace, so this is the one broker tool an agent can call before it knows
// where it is publishing.
//
//	tool, handler := broker.LintTool(client)
//	srv.AddTool(tool, handler)
func LintTool(l Linter) (mcp.Tool, mcpserver.ToolHandlerFunc) {
	tool := mcp.NewTool("noetive_lint",
		mcp.WithDescription("Check a SemQL query for errors and get completion suggestions before running it. Use this when a query is unfamiliar or a search returned an invalid_request error."),
		mcp.WithString("query",
			mcp.Required(),
			mcp.Description("SemQL query to check. May be partial — completions are suggested for the position given by cursor."),
		),
		mcp.WithNumber("cursor",
			mcp.Description("Byte offset into the query to suggest completions for. Defaults to the end of the query."),
			mcp.Min(0),
		),
		mcp.WithReadOnlyHintAnnotation(true),
	)

	handler := func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		query, err := request.RequireString("query")
		if err != nil {
			return mcp.NewToolResultErrorFromErr("noetive_lint: invalid arguments", err), nil
		}

		cursor := request.GetInt("cursor", 0)
		if cursor < 0 || cursor > len(query) {
			return mcp.NewToolResultErrorf("noetive_lint: cursor must be between 0 and %d, got %d", len(query), cursor), nil
		}

		ctx, cancel := context.WithTimeout(ctx, probeTimeout)
		defer cancel()

		resp, err := l.Lint(ctx, semantik.LintRequest{Query: query, Cursor: cursor})
		if err != nil {
			return failure("noetive_lint", err), nil
		}

		result := linted{
			Diagnostics: resp.Diagnostics,
			Completions: resp.Completions,
			Normalized:  resp.Normalized,
			Valid:       resp.Valid,
		}
		return mcp.NewToolResultStructured(result, verdict(result)), nil
	}

	return tool, handler
}

// verdict renders the text fallback. An invalid query says how many problems
// were found so an agent reading only the text still knows to look at the
// structured diagnostics.
func verdict(result linted) string {
	if result.Valid {
		if result.Normalized == "" {
			return "Query is valid."
		}
		return "Query is valid. Normalized form: " + result.Normalized
	}

	if len(result.Diagnostics) == 1 {
		return "Query is not valid: 1 diagnostic."
	}
	return "Query is not valid: " + strconv.Itoa(len(result.Diagnostics)) + " diagnostics."
}
