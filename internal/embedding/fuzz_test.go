package embedding_test

import (
	"context"
	"strings"
	"testing"

	"github.com/noetive/noetive-sdk-go/semantik"

	"github.com/noetive/noetive-mcp/internal/embedding"
)

// A SemQL query is untrusted input in the sense that matters here: it is
// written by a language model, in two syntaxes, and this package hand-rolls a
// scanner over both. The scanner's answer is what the package relies on to
// claim a query carries no text, so a case it walks past is not a parse error
// somebody notices — it is a phrase on the wire.
//
// The broker package's targets cannot reach any of this. They drive the tools
// against a stub, so the rewrite never runs; only a server with an embeddings
// endpoint configured takes this path, and that is exactly the deployment whose
// operator arranged for the text to stay put.

// asTransportDelivers is what a query looks like by the time it reaches a tool
// handler. mcp-go decodes the JSON-RPC frame with encoding/json, which replaces
// every invalid UTF-8 sequence in a string with U+FFFD, so a handler never sees
// a lone continuation byte however the client sends one.
//
// It is applied rather than filtered so the fuzzer's work is never discarded,
// and it is applied at all because the difference is load-bearing: goccy's
// streaming decoder panics with an index out of range on malformed JSON over
// its 1 KiB buffer when the overflow lands mid-sequence in invalid UTF-8, and
// decodeQuery calls it unguarded. Feeding it bytes the transport cannot deliver
// would gate every commit on a crash nothing can reach. Feeding it what the
// transport does deliver is the check worth having — and if that ever stops
// being true, the guard is the one the SDK already wrote: semantik.safeUnmarshal
// wraps the same library in a recover for the same reason.
func asTransportDelivers(query string) string {
	return strings.ToValidUTF8(query, "�")
}

// leakMarker opens every generated phrase. It is placed inside a clause by
// construction in every template below, so finding it in the outgoing query is
// unambiguous: no namespace name, no threshold and no keyword can contain it.
// Anchoring on the prefix rather than the whole phrase keeps the oracle sound
// when the fuzzer puts a quote in the middle and splits the literal in two.
const leakMarker = "NOETIVE_LEAK_"

// clauseTemplates put a phrase where the grammar admits an anchor, plus the
// shapes a malformed query produces around a namespace selector. %s is the
// phrase, already quoted by the template.
var clauseTemplates = []string{
	`MATCH DISTANCE("%s") WITHIN 0.4`,
	`MATCH DIRECTION(["%s", "b"]) CONE 0.3`,
	`MATCH CONTRAST(ATTRACT "%s", REPEL "b") WITHIN 0.45`,
	`MATCH (DISTANCE("%s") OR DIRECTION("b")) NAMESPACE "acme"`,
	`MATCH DISTANCE("a") NAMESPACE "acme" AND DISTANCE "%s"`,
	`MATCH DISTANCE("a") NAMESPACE "acme", NOT "staging" WINDOW 7d DISTANCE "%s"`,
	`{"match":{"distance":{"anchor":"%s","within":0.4}}}`,
	`{"match":{"and":[{"distance":{"anchor":"%s"}},{"not":{"direction":{"toward":["b"]}}}]}}`,
}

// The one property worth stating as a promise: a query that reaches Noetive
// carries no phrase the agent wrote in a clause. Refusing is always an
// acceptable answer — nothing is sent — so the target only ever asserts about
// the calls that succeeded.
func FuzzClauseAnchorsNeverTravel(f *testing.F) {
	f.Add("payroll incident", 0)
	f.Add(`quote " inside`, 0)
	f.Add(`backslash \ inside`, 0)
	f.Add("", 4)
	f.Add(`") OR DISTANCE("injected`, 0)
	f.Add("acquisition of northwind", 5)
	f.Add(strings.Repeat("a", 2000), 6)

	f.Fuzz(func(t *testing.T, phrase string, template int) {
		if template < 0 {
			template = -template
		}
		query := asTransportDelivers(strings.Replace(
			clauseTemplates[template%len(clauseTemplates)],
			"%s", leakMarker+phrase, 1,
		))

		b := &stubBroker{}
		p := embedding.Precomputed(b, &stubEmbedder{})

		if _, err := p.Search(context.Background(), semantik.SearchRequest{
			Query: query, Namespace: "acme", Model: "model-a", Dimensions: 3,
		}); err != nil {
			// Refused before anything was sent, which is the safe answer. That it
			// really was before is the next target's business.
			return
		}
		if strings.Contains(b.searchReq.Query, leakMarker) {
			t.Fatalf("a phrase written inside a clause reached the broker\n in: %q\nout: %q", query, b.searchReq.Query)
		}
	})
}

// A refusal has to mean nothing left, not that the caller was told afterwards.
// Every error path in this package runs before the broker call by construction,
// and this is what keeps it that way as paths are added.
func FuzzARefusedQueryIsNeverSent(f *testing.F) {
	f.Add(`MATCH DISTANCE("a")`)
	f.Add(`MATCH "a"`)
	f.Add(`MATCH DISTANCE("a"`)
	f.Add(`MATCH DISTANCE("a\q")`)
	f.Add(`{"match":{"nearby":{"anchor":"a"}}}`)
	f.Add(`{"match":`)
	f.Add(`{"match":{"distance":{"anchor":"a","mystery":1}}}`)

	f.Fuzz(func(t *testing.T, query string) {
		query = asTransportDelivers(query)
		b := &stubBroker{}
		p := embedding.Precomputed(b, &stubEmbedder{})

		_, err := p.Search(context.Background(), semantik.SearchRequest{
			Query: query, Namespace: "acme", Model: "model-a", Dimensions: 3,
		})
		if err != nil && b.calls != 0 {
			t.Fatalf("the query was refused with %v but %d call(s) had already been made", err, b.calls)
		}
	})
}

// Both syntaxes reach a parser on arbitrary text, and the JSON one is a vendor
// decoder rather than code in this repo. A panic in either takes down the
// editor session rather than failing the one call, so it is worth a target of
// its own even where no input is known to cause one.
func FuzzQueryParsersNeverPanic(f *testing.F) {
	f.Add(`MATCH DISTANCE("a") NAMESPACE "acme" WINDOW 7d LIMIT 5`, 0)
	f.Add(`MATCH ((((DISTANCE("a")`, 3)
	f.Add(`MATCH DISTANCE("a\`, 0)
	f.Add(`))))`, 0)
	f.Add(`{"match":{"distance":{"anchor":"`+strings.Repeat("x", 1100)+`"`, 0)
	f.Add(`{"match":{"or":[`+strings.Repeat(`{"distance":{"anchor":"x"}},`, 40), 0)
	f.Add(`{`, -1)
	f.Add("\ufeff\x00", 0)

	f.Fuzz(func(t *testing.T, query string, cursor int) {
		query = asTransportDelivers(query)
		p := embedding.Precomputed(&stubBroker{}, &stubEmbedder{})

		// Both entry points, because they scan the same text under different
		// rules: Search rewrites and must understand everything, Lint blanks and
		// is deliberately forgiving of a query still being typed.
		_, _ = p.Search(context.Background(), semantik.SearchRequest{
			Query: query, Namespace: "acme", Model: "model-a", Dimensions: 3,
		})
		_, _ = p.Lint(context.Background(), semantik.LintRequest{Query: query, Cursor: cursor})
	})
}
