package embedding_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/noetive/noetive-sdk-go/semantik"

	"github.com/noetive/noetive-mcp/internal/embedding"
)

// stubEmbedder stands in for a service on this machine.
//
// Each vector's first element is its text's position in the batch plus one, so
// a test can say which phrase landed on which anchor by reading one number
// rather than by pinning float values it would have to keep in step.
type stubEmbedder struct {
	err    error
	model  string
	texts  []string
	calls  int
	short  bool
	vector []float32
}

func (s *stubEmbedder) Embed(_ context.Context, model string, dimensions uint16, texts []string) ([][]float32, error) {
	s.calls++
	s.model = model
	s.texts = append([]string(nil), texts...)

	if s.err != nil {
		return nil, s.err
	}

	out := make([][]float32, 0, len(texts))
	for i := range texts {
		if s.vector != nil {
			out = append(out, s.vector)
			continue
		}
		v := make([]float32, dimensions)
		if len(v) > 0 {
			v[0] = float32(i + 1)
		}
		out = append(out, v)
	}
	if s.short && len(out) > 0 {
		out = out[:len(out)-1]
	}
	return out, nil
}

// stubBroker records the last request of each kind and reports how many calls
// reached it, so a test can assert both what was sent and that nothing was.
type stubBroker struct {
	lintResp     semantik.LintResponse
	publishReq   semantik.PublishRequest
	searchReq    semantik.SearchRequest
	subscribeReq semantik.SubscribeRequest
	lintReq      semantik.LintRequest
	calls        int
}

func (s *stubBroker) Publish(_ context.Context, req semantik.PublishRequest) (semantik.PublishResponse, error) {
	s.calls++
	s.publishReq = req
	return semantik.PublishResponse{MessageID: "msg_01"}, nil
}

func (s *stubBroker) Search(_ context.Context, req semantik.SearchRequest) (semantik.SearchResponse, error) {
	s.calls++
	s.searchReq = req
	return semantik.SearchResponse{}, nil
}

func (s *stubBroker) Subscribe(_ context.Context, req semantik.SubscribeRequest) (*semantik.Subscription, error) {
	s.calls++
	s.subscribeReq = req
	// *semantik.Subscription cannot be built outside its package, and no test
	// here needs one: reaching this method at all is the fact being asserted.
	return nil, errors.New("opened")
}

func (s *stubBroker) Lint(_ context.Context, req semantik.LintRequest) (semantik.LintResponse, error) {
	s.calls++
	s.lintReq = req
	return s.lintResp, nil
}

func (s *stubBroker) Health(context.Context) error {
	s.calls++
	return nil
}

// target is a fully-specified routing triple, standing in for a namespace an
// operator provisioned.
const (
	model = "Qwen3-Embedding-4B"
	dims  = uint16(3)
)

func searchWith(t *testing.T, b *stubBroker, e embedding.Embedder, query string) {
	t.Helper()
	if _, err := embedding.Precomputed(b, e).Search(context.Background(), semantik.SearchRequest{
		Query: query, Namespace: "incidents", Model: model, Dimensions: dims,
	}); err != nil {
		t.Fatalf("search: %v", err)
	}
}

func searchError(t *testing.T, b *stubBroker, e embedding.Embedder, query string) string {
	t.Helper()
	_, err := embedding.Precomputed(b, e).Search(context.Background(), semantik.SearchRequest{
		Query: query, Namespace: "incidents", Model: model, Dimensions: dims,
	})
	if err == nil {
		t.Fatalf("expected %q to be refused, but it reached the broker as %q", query, b.searchReq.Query)
	}
	return err.Error()
}

// A publish carries both. The vector is what the message is indexed by, and the
// text is what a search gives back — drop either and half the point goes with
// it: no vector means the broker embeds after all, and no text means every hit
// comes back contentless.
func TestPublishSendsTheVectorAndKeepsTheText(t *testing.T) {
	b, e := &stubBroker{}, &stubEmbedder{}

	const message = "the gateway returns 202 before the write lands"

	if _, err := embedding.Precomputed(b, e).Publish(context.Background(), semantik.PublishRequest{
		Items:      []semantik.PublishItem{{Text: message}},
		Namespace:  "incidents",
		Model:      model,
		Dimensions: dims,
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}

	item := b.publishReq.Items[0]
	if item.Text != message {
		t.Errorf("expected the text to survive so a search can return it, got %q", item.Text)
	}
	if len(item.Vector) != int(dims) {
		t.Fatalf("expected a %d-element vector, got %d", dims, len(item.Vector))
	}
	if e.model != model {
		t.Errorf("expected the endpoint to be asked for %q, got %q", model, e.model)
	}
}

// The SDK does not copy Vector, so the caller still holds the slice it passed.
// Writing into their backing array is a side effect nothing in the signature
// admits to, and it would surface as a mutated request on a retry.
func TestPublishDoesNotMutateTheCallersItems(t *testing.T) {
	b, e := &stubBroker{}, &stubEmbedder{}

	items := []semantik.PublishItem{{Text: "a conclusion worth sharing"}}
	if _, err := embedding.Precomputed(b, e).Publish(context.Background(), semantik.PublishRequest{
		Items: items, Namespace: "incidents", Model: model, Dimensions: dims,
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}

	if items[0].Text == "" || len(items[0].Vector) != 0 {
		t.Fatalf("the caller's item was rewritten in place: %+v", items[0])
	}
}

// An embedding the caller computed is theirs. Re-embedding it would replace a
// vector they chose with one from a model they did not ask for.
func TestPublishLeavesAnItemThatAlreadyCarriesAVector(t *testing.T) {
	b, e := &stubBroker{}, &stubEmbedder{}

	mine := []float32{0.25, 0.5, 0.75}
	if _, err := embedding.Precomputed(b, e).Publish(context.Background(), semantik.PublishRequest{
		Items:      []semantik.PublishItem{{Vector: mine, Text: "ignored"}},
		Namespace:  "incidents",
		Model:      model,
		Dimensions: dims,
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}

	if e.calls != 0 {
		t.Errorf("the endpoint was called for an item that already had a vector")
	}
	if got := b.publishReq.Items[0].Vector; len(got) != 3 || got[0] != 0.25 {
		t.Errorf("the caller's vector was replaced: %v", got)
	}
}

// A failed publish must be a publish that did not happen. Reaching the broker
// with the text still in the item would be the exact leak this exists to stop,
// and the agent would have no way to know it had occurred.
func TestPublishNeverReachesNoetiveWhenEmbeddingFails(t *testing.T) {
	b := &stubBroker{}
	e := &stubEmbedder{err: errors.New("connection refused")}

	_, err := embedding.Precomputed(b, e).Publish(context.Background(), semantik.PublishRequest{
		Items: []semantik.PublishItem{{Text: "a conclusion"}}, Namespace: "incidents", Model: model, Dimensions: dims,
	})
	if err == nil {
		t.Fatal("expected the publish to fail")
	}
	if b.calls != 0 {
		t.Fatal("the message reached Noetive despite the embedding failing")
	}
}

func TestSearchNeverReachesNoetiveWhenEmbeddingFails(t *testing.T) {
	b := &stubBroker{}
	e := &stubEmbedder{err: errors.New("connection refused")}

	searchError(t, b, e, `MATCH DISTANCE("payroll incident") WITHIN 0.5`)

	if b.calls != 0 {
		t.Fatal("the query reached Noetive despite the embedding failing")
	}
}

func TestSubscribeNeverReachesNoetiveWhenEmbeddingFails(t *testing.T) {
	b := &stubBroker{}
	e := &stubEmbedder{err: errors.New("connection refused")}

	if _, err := embedding.Precomputed(b, e).Subscribe(context.Background(), semantik.SubscribeRequest{
		Query: `MATCH DISTANCE("gpu shortage") WITHIN 0.5`, Namespace: "incidents", Model: model, Dimensions: dims,
	}); err == nil {
		t.Fatal("expected the subscribe to fail")
	}
	if b.calls != 0 {
		t.Fatal("the query reached Noetive despite the embedding failing")
	}
}

// A short batch means one anchor has no vector. Applying the rest would send a
// query that still carries a phrase, so the whole call is refused instead.
func TestAShortBatchIsRefusedRatherThanPartiallyApplied(t *testing.T) {
	b := &stubBroker{}
	e := &stubEmbedder{short: true}

	searchError(t, b, e, `MATCH DISTANCE("alpha") WITHIN 0.4 AND DISTANCE("beta") WITHIN 0.4`)

	if b.calls != 0 {
		t.Fatal("a partially rewritten query reached Noetive")
	}
}

func TestSubscribeRewritesItsQueryToo(t *testing.T) {
	b, e := &stubBroker{}, &stubEmbedder{}

	// The stub cannot return a *semantik.Subscription, so the error is expected
	// and what matters is the query that reached it.
	_, _ = embedding.Precomputed(b, e).Subscribe(context.Background(), semantik.SubscribeRequest{
		Query: `MATCH DISTANCE("gpu shortage") WITHIN 0.5`, Namespace: "incidents", Model: model, Dimensions: dims,
	})

	if strings.Contains(b.subscribeReq.Query, "gpu shortage") {
		t.Fatalf("the anchor text reached the wire: %s", b.subscribeReq.Query)
	}
	if want := `MATCH DISTANCE([1,0,0]) WITHIN 0.5`; b.subscribeReq.Query != want {
		t.Errorf("expected %q, got %q", want, b.subscribeReq.Query)
	}
}

// Lint is the one call that ships a query without running it, and it is
// unauthenticated. Left alone it would be a way for every anchor an agent ever
// typed to reach Noetive on a server whose whole point is that they do not.
func TestLintSendsTheQueryWithItsAnchorTextRemoved(t *testing.T) {
	b, e := &stubBroker{}, &stubEmbedder{}
	b.lintResp = semantik.LintResponse{Valid: true, Normalized: `MATCH DISTANCE("x") WITHIN 0.5`}

	resp, err := embedding.Precomputed(b, e).Lint(context.Background(), semantik.LintRequest{
		Query: `MATCH DISTANCE("payroll incident") WITHIN 0.5`,
	})
	if err != nil {
		t.Fatalf("lint: %v", err)
	}

	if strings.Contains(b.lintReq.Query, "payroll") {
		t.Fatalf("the anchor text reached the lint endpoint: %s", b.lintReq.Query)
	}
	if want := `MATCH DISTANCE("x") WITHIN 0.5`; b.lintReq.Query != want {
		t.Errorf("expected %q, got %q", want, b.lintReq.Query)
	}
	// Returning the normalized form would hand back a query with the
	// placeholder where the anchors were, and an agent would write it down.
	if resp.Normalized != "" {
		t.Errorf("expected no normalized form, got %q", resp.Normalized)
	}
	if e.calls != 0 {
		t.Errorf("lint has no model or dimensionality, so it must not embed")
	}
	// The missing normalized form and the approximate completion positions are
	// consequences an agent has no way to infer, so they are said rather than
	// left as a silent difference from what the tool usually returns.
	var told bool
	for _, d := range resp.Diagnostics {
		if d.Severity == "info" && strings.Contains(d.Message, "anchor text replaced") {
			told = true
		}
	}
	if !told {
		t.Errorf("expected the result to say the query was checked with its text removed, got %+v", resp.Diagnostics)
	}
}

// A half-typed query is the normal case for lint. Refusing it would remove the
// diagnostic at the moment it is most needed.
func TestLintWorksOnAQueryThatIsStillBeingTyped(t *testing.T) {
	b, e := &stubBroker{}, &stubEmbedder{}

	if _, err := embedding.Precomputed(b, e).Lint(context.Background(), semantik.LintRequest{
		Query: `MATCH DISTANCE("payroll inci`,
	}); err != nil {
		t.Fatalf("lint: %v", err)
	}

	if strings.Contains(b.lintReq.Query, "payroll") {
		t.Fatalf("the anchor text reached the lint endpoint: %s", b.lintReq.Query)
	}
	// The literal is left unterminated, because that is the diagnostic.
	if want := `MATCH DISTANCE("x`; b.lintReq.Query != want {
		t.Errorf("expected %q, got %q", want, b.lintReq.Query)
	}
}

// Cursor is a byte offset into the query the agent wrote. Blanking moves the
// bytes after the first anchor, and an offset past the end is rejected by the
// SDK before the request is sent.
func TestLintClampsACursorThatBlankingPutOutOfBounds(t *testing.T) {
	b, e := &stubBroker{}, &stubEmbedder{}

	query := `MATCH DISTANCE("payroll incident") WITHIN 0.5`
	if _, err := embedding.Precomputed(b, e).Lint(context.Background(), semantik.LintRequest{
		Query: query, Cursor: len(query),
	}); err != nil {
		t.Fatalf("lint: %v", err)
	}

	if b.lintReq.Cursor > len(b.lintReq.Query) {
		t.Errorf("cursor %d is past the end of the %d-byte query that was sent", b.lintReq.Cursor, len(b.lintReq.Query))
	}
}

// Health says whether Noetive is reachable. Probing the local endpoint here
// would need a model and a dimensionality it does not have, and a bare
// reachability ping would report healthy for a service serving the wrong model.
func TestHealthPassesStraightThrough(t *testing.T) {
	b, e := &stubBroker{}, &stubEmbedder{}

	if err := embedding.Precomputed(b, e).Health(context.Background()); err != nil {
		t.Fatalf("health: %v", err)
	}
	if b.calls != 1 {
		t.Errorf("expected health to reach the broker once, got %d calls", b.calls)
	}
	if e.calls != 0 {
		t.Errorf("health must not embed")
	}
}
