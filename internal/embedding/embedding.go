// Package embedding computes embeddings on this machine, so an operator can
// choose which model represents their meaning rather than taking the broker's.
//
// Without it, every publish, search and subscribe sends text and the broker
// embeds it. With an endpoint configured, the vector is computed here: a
// publish carries it alongside the message, and a query carries a SemQL vector
// literal in place of each anchor phrase. The lint call, which exists to check
// a query rather than run it, has its anchor text replaced instead.
//
// What this does and does not keep back is worth stating exactly, because the
// two halves differ:
//
//   - Query anchor phrases do not leave. What an agent is looking for stays
//     here, on search, on subscribe and on lint alike.
//   - Message text does leave, and is stored. A publish sends the vector and
//     the text together, because the vector is what the message is indexed by
//     and the text is what a search gives back. Sending the vector alone would
//     keep the body off the network and leave every hit contentless.
//
// So the reasons to turn this on are the model, the latency and the queries:
// embeddings from a model you chose, a publish that does not wait on the
// broker's embedder, and searches that do not announce what you are looking
// for. It is not a way to publish confidentially.
//
// The decision is the operator's and only the operator's. There is no tool
// argument that turns it off, because the caller is a language model and a
// switch it can reach is not a guarantee — one sentence of prompt injection
// would be enough to route around it. There is likewise no fallback to
// server-side embedding when the endpoint is unreachable: falling back would
// silently hand the work to a different model, which is the one thing nothing
// downstream can detect. A call that cannot be embedded here fails, and nothing
// is sent.
//
// One name, one model. The model and the dimensionality come from the routing
// triple on every call and are handed to the endpoint unchanged, so there is no
// second setting that could disagree with the first. The endpoint has to answer
// to the name the namespace uses; a name it does not know is an immediate 404
// quoting the string we asked for. What cannot be checked from here is whether
// a service answering to the right name is serving the right model: dimensions
// would agree while the vector space did not, every publish would land where
// nothing finds it, and every query would return confident nonsense. That one
// is the operator's to get right.
package embedding

import (
	"context"
	"fmt"

	"github.com/noetive/noetive-sdk-go/semantik"

	"github.com/noetive/noetive-mcp/internal/broker"
)

// Embedder turns phrases into vectors in the namespace's own space.
//
// The order of the result is the contract: vector i belongs to text i. A
// reordered or short answer is what silently anchors a query on the wrong
// phrase, and every result would still look plausible.
type Embedder interface {
	Embed(ctx context.Context, model string, dimensions uint16, texts []string) ([][]float32, error)
}

// Broker is the set of Semantik operations Precomputed decorates.
//
// It is composed from the narrow interfaces the tools already declare, so no
// method signature is written twice. *semantik.Client satisfies it, which is
// the only thing it is ever handed in production.
type Broker interface {
	broker.Publisher
	broker.Searcher
	broker.Linter
	broker.HealthChecker

	Subscribe(ctx context.Context, req semantik.SubscribeRequest) (*semantik.Subscription, error)
}

// Precomputed wraps a Semantik client so every request that would have carried
// text carries a vector instead.
//
// "Precomputed" is the SDK's own word for this case: PublishItem documents that
// a supplied vector takes precedence and spares the server an embed call.
//
//	srv := mcpserver.New(version, embedding.Precomputed(client, endpoint), policy)
func Precomputed(b Broker, e Embedder) Broker {
	return &precomputed{broker: b, embed: e}
}

// precomputed delegates method by method rather than embedding Broker. If
// Broker ever grows a sixth operation that carries text, this type stops
// satisfying it and the build says so — where an embedded interface would
// quietly forward the new call with the text still in it.
//
// Field ordering: interfaces (16 B each).
type precomputed struct {
	broker Broker
	embed  Embedder
}

// Publish attaches the vector to each item and sends the text with it.
//
// Both, not one. The vector is what the message is indexed by — PublishItem
// documents that a supplied vector takes precedence and the server does not
// embed the text — and the text is what noetive_search gives back when someone
// finds it later. Sending the vector alone would keep the message text off the
// network, but it would also leave every hit contentless, which is most of what
// makes search worth calling.
//
// So this is not a confidentiality measure for message bodies: the text still
// reaches Noetive and is still stored. What it buys is the choice of which
// model does the embedding, a publish that does not wait on the broker's
// embedder, and anchor phrases that stay here — see the package doc.
func (p *precomputed) Publish(ctx context.Context, req semantik.PublishRequest) (semantik.PublishResponse, error) {
	// The SDK does not copy Vector, and the caller still holds this slice.
	// Writing into their backing array would be a side effect nothing in the
	// signature admits to.
	items := make([]semantik.PublishItem, len(req.Items))
	copy(items, req.Items)

	var (
		texts []string
		slots []int
	)
	for i, item := range items {
		// An item that already carries a vector is the caller's own embedding
		// and is left exactly as they computed it.
		if item.Text == "" || len(item.Vector) > 0 {
			continue
		}
		texts = append(texts, item.Text)
		slots = append(slots, i)
	}

	if len(texts) > 0 {
		vectors, err := p.embed.Embed(ctx, req.Model, req.Dimensions, texts)
		if err != nil {
			return semantik.PublishResponse{}, err
		}
		if len(vectors) != len(texts) {
			return semantik.PublishResponse{}, fmt.Errorf("asked for %d embeddings and got %d, so nothing was published", len(texts), len(vectors))
		}
		for j, i := range slots {
			items[i].Vector = vectors[j]
		}
	}

	req.Items = items
	return p.broker.Publish(ctx, req)
}

// Search rewrites the query's anchors into vectors before it is sent.
func (p *precomputed) Search(ctx context.Context, req semantik.SearchRequest) (semantik.SearchResponse, error) {
	query, err := rewrite(ctx, p.embed, req.Model, req.Dimensions, req.Query)
	if err != nil {
		return semantik.SearchResponse{}, err
	}
	req.Query = query
	return p.broker.Search(ctx, req)
}

// Subscribe rewrites the query's anchors into vectors before it is sent.
func (p *precomputed) Subscribe(ctx context.Context, req semantik.SubscribeRequest) (*semantik.Subscription, error) {
	query, err := rewrite(ctx, p.embed, req.Model, req.Dimensions, req.Query)
	if err != nil {
		return nil, err
	}
	req.Query = query
	return p.broker.Subscribe(ctx, req)
}

// Lint sends the query with its anchor text replaced by a placeholder.
//
// Lint carries no routing triple, so there is no model and no dimensionality to
// embed with — and a query being linted is often one that does not parse yet,
// which is precisely when there is nothing to embed. Blanking checks the shape,
// which is what lint is for, while the phrases stay here.
func (p *precomputed) Lint(ctx context.Context, req semantik.LintRequest) (semantik.LintResponse, error) {
	blanked, err := blank(req.Query)
	if err != nil {
		return semantik.LintResponse{}, err
	}

	// Cursor is a byte offset into the query the agent wrote, and replacing a
	// phrase with one character moves every byte after it. Clamping is honest
	// about that; carrying the offset over would point at a position that means
	// something else now.
	if req.Cursor > len(blanked) {
		req.Cursor = len(blanked)
	}
	req.Query = blanked

	resp, err := p.broker.Lint(ctx, req)
	if err != nil {
		return semantik.LintResponse{}, err
	}

	// Normalized comes back with the placeholder standing where the anchors
	// were. Handing that to an agent would read as a corrected version of their
	// query, and they would write it down.
	resp.Normalized = ""
	resp.Diagnostics = append(resp.Diagnostics, semantik.LintDiagnostic{
		Severity: "info",
		Message:  "This server embeds locally, so the query was checked with its anchor text replaced. The grammar was checked; the phrases were not sent, no normalized form is returned, and completion positions are approximate.",
	})

	return resp, nil
}

// Health passes straight through.
//
// It deliberately does not probe the embeddings endpoint. Health carries no
// model and no dimensionality, so the only thing it could send is a bare
// reachability ping — which would report healthy for a service that cannot
// serve the namespace's model, the one failure worth catching.
func (p *precomputed) Health(ctx context.Context) error {
	return p.broker.Health(ctx)
}
