package broker

import (
	"context"
	"strconv"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/noetive/noetive-sdk-go/semantik"

	"github.com/noetive/noetive-mcp/internal/targeting"
)

// Searcher evaluates a SemQL query across a namespace.
type Searcher interface {
	Search(ctx context.Context, req semantik.SearchRequest) (semantik.SearchResponse, error)
}

// match is one ranked hit. Unlike a subscribe match, a search hit carries the
// message content, so an agent can read the result without a second call.
//
// Field ordering: map (8 B) > strings (16 B each) > float32 (4 B).
type match struct {
	Metadata  map[string]string `json:"metadata,omitempty"`
	Content   string            `json:"content"`
	MessageID string            `json:"message_id"`
	Namespace string            `json:"namespace,omitempty"`
	Score     float32           `json:"score"`
}

// searchResults is the structured result of a search.
type searchResults struct {
	Results []match `json:"results"`
}

// SearchTool builds the noetive_search tool and its handler.
//
//	tool, handler := broker.SearchTool(client, configured)
//	srv.AddTool(tool, handler)
func SearchTool(s Searcher, fallback targeting.Target) (mcp.Tool, mcpserver.ToolHandlerFunc) {
	options := []mcp.ToolOption{
		mcp.WithDescription("Search a Noetive Semantik namespace with a SemQL query and get back ranked messages with their content and metadata. Use this to find what other agents already learned instead of rediscovering it. Run noetive_lint first if you are unsure the query parses."),
		mcp.WithString("query",
			mcp.Required(),
			mcp.Description("SemQL query, for example: MATCH DISTANCE(\"payment reconciliation\") WITHIN 0.4 LIMIT 10"),
		),
		mcp.WithNumber("limit",
			mcp.Description("Maximum number of results. Overrides any LIMIT clause in the query; when omitted the query's own LIMIT applies."),
			mcp.Min(1),
		),
		mcp.WithReadOnlyHintAnnotation(true),
	}
	options = append(options, targetingOptions()...)

	tool := mcp.NewTool("noetive_search", options...)

	handler := func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		query, err := request.RequireString("query")
		if err != nil {
			return mcp.NewToolResultErrorFromErr("noetive_search: invalid arguments", err), nil
		}

		requested, err := requestedTarget(request)
		if err != nil {
			return mcp.NewToolResultErrorFromErr("noetive_search: invalid arguments", err), nil
		}
		target, err := targeting.Resolve(requested, fallback)
		if err != nil {
			return mcp.NewToolResultErrorFromErr("noetive_search", err), nil
		}

		limit := request.GetInt("limit", 0)
		if limit < 0 {
			return mcp.NewToolResultErrorf("noetive_search: limit must be positive, got %d", limit), nil
		}

		ctx, cancel := context.WithTimeout(ctx, queryTimeout)
		defer cancel()

		resp, err := s.Search(ctx, semantik.SearchRequest{
			Query:      query,
			Namespace:  target.Namespace,
			Model:      target.Model,
			Limit:      limit,
			Dimensions: target.Dimensions,
		})
		if err != nil {
			return failure("noetive_search", err), nil
		}

		results := searchResults{Results: make([]match, 0, len(resp.Results))}
		for _, r := range resp.Results {
			results.Results = append(results.Results, match{
				Metadata:  r.Metadata,
				Content:   r.Content,
				MessageID: r.MessageID,
				Namespace: r.Namespace,
				Score:     r.Score,
			})
		}

		return mcp.NewToolResultStructured(results, summarize(target.Namespace, results.Results)), nil
	}

	return tool, handler
}

// summarize renders the text fallback for clients that cannot read structured
// content. An empty result set says so explicitly rather than returning nothing,
// because an agent reading a blank result cannot tell a miss from a failure.
func summarize(namespace string, matches []match) string {
	// Concatenation rather than Sprintf: search is the most-called tool, and
	// this runs on every call including the ones that find nothing.
	switch len(matches) {
	case 0:
		return "No matches in " + namespace + "."
	case 1:
		return "1 match in " + namespace + "."
	default:
		return strconv.Itoa(len(matches)) + " matches in " + namespace + "."
	}
}
