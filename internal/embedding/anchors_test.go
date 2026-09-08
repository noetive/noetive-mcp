package embedding_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/noetive/noetive-sdk-go/semantik"

	"github.com/noetive/noetive-mcp/internal/embedding"
)

// Every anchor position the grammar admits must be rewritten. One missed
// position is one phrase that still travels, and the query would still work —
// so nothing would ever point at the gap.
func TestEveryClauseKindHasItsAnchorsReplaced(t *testing.T) {
	for _, c := range []struct {
		name  string
		query string
		want  string
	}{
		{
			name:  "distance",
			query: `MATCH DISTANCE("payroll incident") WITHIN 0.5`,
			want:  `MATCH DISTANCE([1,0,0]) WITHIN 0.5`,
		},
		{
			name:  "direction with one anchor",
			query: `MATCH DIRECTION("customer frustration") CONE 0.4`,
			want:  `MATCH DIRECTION([1,0,0]) CONE 0.4`,
		},
		{
			name:  "direction with a list",
			query: `MATCH DIRECTION(["customer frustration", "billing complaint"]) CONE 0.4`,
			want:  `MATCH DIRECTION([[1,0,0], [2,0,0]]) CONE 0.4`,
		},
		{
			name:  "contrast",
			query: `MATCH CONTRAST(ATTRACT ["enterprise"], REPEL ["free tier"]) WITHIN 0.45`,
			want:  `MATCH CONTRAST(ATTRACT [[1,0,0]], REPEL [[2,0,0]]) WITHIN 0.45`,
		},
		{
			name:  "nested through boolean operators and a group",
			query: `MATCH (DISTANCE("alpha") WITHIN 0.4 OR NOT DISTANCE("beta") WITHIN 0.4) AND DISTANCE("gamma") TOP 5`,
			want:  `MATCH (DISTANCE([1,0,0]) WITHIN 0.4 OR NOT DISTANCE([2,0,0]) WITHIN 0.4) AND DISTANCE([3,0,0]) TOP 5`,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			b, e := &stubBroker{}, &stubEmbedder{}
			searchWith(t, b, e, c.query)

			if strings.Contains(b.searchReq.Query, `"`) {
				t.Fatalf("a quoted phrase survived the rewrite: %s", b.searchReq.Query)
			}
			if b.searchReq.Query != c.want {
				t.Errorf("expected\n  %s\ngot\n  %s", c.want, b.searchReq.Query)
			}
		})
	}
}

// The splice must leave everything that is not an anchor exactly as the agent
// wrote it — brackets, keywords, thresholds, spacing — so that a rejection from
// the broker quotes a query they recognise.
func TestOnlyTheAnchorsChange(t *testing.T) {
	b, e := &stubBroker{}, &stubEmbedder{}

	searchWith(t, b, e, `MATCH DISTANCE("alpha") WITHIN 0.5 AND NOT DIRECTION(["beta", "gamma"]) CONE 0.2 WINDOW 7d LIMIT 20`)

	want := `MATCH DISTANCE([1,0,0]) WITHIN 0.5 AND NOT DIRECTION([[2,0,0], [3,0,0]]) CONE 0.2 WINDOW 7d LIMIT 20`
	if b.searchReq.Query != want {
		t.Errorf("expected\n  %s\ngot\n  %s", want, b.searchReq.Query)
	}
}

// The literal has to carry the vector the endpoint produced, to the precision
// it produced it at. Rendering 0.125 as 1 would move every anchor to a
// different point while leaving a query that still parses and still returns
// results — plausible ones, for a question nobody asked.
func TestTheVectorLiteralKeepsEveryDigit(t *testing.T) {
	b := &stubBroker{}
	e := &stubEmbedder{vector: []float32{0.125, -0.5, 0.0009765625}}

	searchWith(t, b, e, `MATCH DISTANCE("alpha") WITHIN 0.5`)

	want := `MATCH DISTANCE([0.125,-0.5,0.0009765625]) WITHIN 0.5`
	if b.searchReq.Query != want {
		t.Errorf("expected\n  %s\ngot\n  %s", want, b.searchReq.Query)
	}
}

// A query that exactly fills the budget must be sent. A ceiling that refuses
// the last legal query is as wrong as one that admits an illegal one, and the
// error would tell the agent to shorten a query that was already short enough.
func TestAQueryThatExactlyFillsTheBudgetIsAllowed(t *testing.T) {
	b, e := &stubBroker{}, &stubEmbedder{}

	var clauses []string
	for _, phrase := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		clauses = append(clauses, `DISTANCE("`+phrase+`") WITHIN 0.4`)
	}

	// Eight anchors at 1024 dimensions is 8192 floats, which is the limit
	// exactly rather than one past it.
	if _, err := embedding.Precomputed(b, e).Search(context.Background(), semantik.SearchRequest{
		Query: "MATCH " + strings.Join(clauses, " AND "), Namespace: "incidents", Model: model, Dimensions: 1024,
	}); err != nil {
		t.Fatalf("expected eight anchors at 1024 dimensions to be allowed, got: %v", err)
	}
	if b.calls != 1 {
		t.Error("the query did not reach Noetive")
	}
}

// The largest vector the wire accepts must still be accepted here.
func TestTheLargestAllowedDimensionalityIsNotRefused(t *testing.T) {
	b, e := &stubBroker{}, &stubEmbedder{}

	if _, err := embedding.Precomputed(b, e).Search(context.Background(), semantik.SearchRequest{
		Query: `MATCH DISTANCE("alpha") WITHIN 0.4`, Namespace: "incidents", Model: model, Dimensions: 4096,
	}); err != nil {
		t.Fatalf("expected 4096 dimensions to be allowed, got: %v", err)
	}
}

// A namespace name is not an anchor. Rewriting one would turn a valid selector
// into a vector where a name belongs, and the query would stop parsing — while
// an anchor that merely reads like a namespace must still be rewritten.
func TestNamespaceNamesAreLeftAloneAndAnchorsAreNot(t *testing.T) {
	b, e := &stubBroker{}, &stubEmbedder{}

	searchWith(t, b, e, `MATCH DISTANCE("global warming") WITHIN 0.5 NAMESPACE "acme-*", NOT "acme-staging"`)

	want := `MATCH DISTANCE([1,0,0]) WITHIN 0.5 NAMESPACE "acme-*", NOT "acme-staging"`
	if b.searchReq.Query != want {
		t.Errorf("expected\n  %s\ngot\n  %s", want, b.searchReq.Query)
	}
	if e.calls != 1 || len(e.texts) != 1 || e.texts[0] != "global warming" {
		t.Errorf("expected only the anchor to be embedded, got %v", e.texts)
	}
}

// Whether a bare phrase is a namespace name turns on recognising the keyword
// that opens the selector, and getting that wrong goes one of two bad ways: a
// missed NAMESPACE refuses a valid query, and a keyword seen where there is
// none lets a phrase through as if it were a name.
func TestTheNamespaceKeywordIsRecognisedExactly(t *testing.T) {
	for _, c := range []struct {
		name    string
		query   string
		refused bool
	}{
		{"upper case", `MATCH DISTANCE("a") WITHIN 0.4 NAMESPACE "acme"`, false},
		{"lower case", `MATCH DISTANCE("a") WITHIN 0.4 namespace "acme"`, false},
		{"mixed case", `MATCH DISTANCE("a") WITHIN 0.4 NameSpace "acme"`, false},
		{"the selector runs to the end", `MATCH DISTANCE("a") WITHIN 0.4 NAMESPACE "acme", NOT "acme-staging"`, false},
		{"a window closes the selector", `MATCH DISTANCE("a") WITHIN 0.4 NAMESPACE "acme" WINDOW 7d`, false},
		// A word that merely ends with the keyword is not the keyword, and a
		// phrase after it is a phrase.
		{"a longer word ending in the keyword", `MATCH DISTANCE("a") WITHIN 0.4 MYNAMESPACE "acme"`, true},
		{"a longer word starting with the keyword", `MATCH DISTANCE("a") WITHIN 0.4 NAMESPACES "acme"`, true},
		// The selector is a list of names, so any word that is not one closes it.
		// A word nobody can parse closes it too: a phrase sitting after some
		// unrecognised token is a query that does not parse, not a second name.
		{"an unrecognised word closes the selector", `MATCH DISTANCE("a") WITHIN 0.4 NAMESPACE "acme" NOLIMIT "beta"`, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			b, e := &stubBroker{}, &stubEmbedder{}

			if c.refused {
				searchError(t, b, e, c.query)
				return
			}
			searchWith(t, b, e, c.query)
			if !strings.Contains(b.searchReq.Query, `"acme"`) {
				t.Errorf("expected the namespace name to survive, got %s", b.searchReq.Query)
			}
		})
	}
}

// The namespace selector must not leave a hole behind it. It used to stay open
// from NAMESPACE until WINDOW or LIMIT, so a phrase that landed anywhere in
// between — which is what a forgotten bracket after a namespace produces — was
// read as a second namespace name and forwarded with its text intact. The
// broker rejects the query either way; the phrase had already travelled, which
// is the one outcome this package exists to prevent.
func TestAPhraseAfterANamespaceIsNeverForwardedAsAName(t *testing.T) {
	for _, query := range []string{
		`MATCH DISTANCE("a") NAMESPACE "acme" AND DISTANCE "acquisition of northwind"`,
		`MATCH DISTANCE("a") NAMESPACE "acme" WINDOW 7d DISTANCE "acquisition of northwind"`,
		`MATCH DISTANCE("a") NAMESPACE "acme", NOT "staging" ORDER BY "acquisition of northwind"`,
	} {
		t.Run(query, func(t *testing.T) {
			b, e := &stubBroker{}, &stubEmbedder{}

			searchError(t, b, e, query)

			if strings.Contains(b.searchReq.Query, "northwind") {
				t.Errorf("the phrase reached the broker: %s", b.searchReq.Query)
			}
		})
	}
}

// Two occurrences of one phrase must cost one embed call and land on the same
// point. Embedding twice would spend an inference for nothing, and a batching
// service is free to answer the two identically or not.
func TestARepeatedPhraseIsEmbeddedOnceAndAppliedTwice(t *testing.T) {
	b, e := &stubBroker{}, &stubEmbedder{}

	searchWith(t, b, e, `MATCH DISTANCE("rollback") WITHIN 0.4 OR DIRECTION(["rollback", "incident"]) CONE 0.3`)

	if e.calls != 1 {
		t.Errorf("expected one call to the endpoint, got %d", e.calls)
	}
	if len(e.texts) != 2 {
		t.Fatalf("expected two distinct phrases, got %v", e.texts)
	}
	want := `MATCH DISTANCE([1,0,0]) WITHIN 0.4 OR DIRECTION([[1,0,0], [2,0,0]]) CONE 0.3`
	if b.searchReq.Query != want {
		t.Errorf("expected\n  %s\ngot\n  %s", want, b.searchReq.Query)
	}
}

// A query already written in vectors is one nobody needs us for. Passing it
// through untouched keeps the bytes the agent chose, and spends nothing.
func TestAQueryThatIsAlreadyVectorsIsPassedThroughUnchanged(t *testing.T) {
	b, e := &stubBroker{}, &stubEmbedder{}

	query := `MATCH DISTANCE([0.1,0.2,0.3]) WITHIN 0.5`
	searchWith(t, b, e, query)

	if b.searchReq.Query != query {
		t.Errorf("expected the query to arrive unchanged, got %s", b.searchReq.Query)
	}
	if e.calls != 0 {
		t.Errorf("expected no embed call, got %d", e.calls)
	}
}

// A query this server cannot fully account for may hold text it did not see.
// Sending it anyway would break the one promise the feature makes, and would do
// it silently, so each of these is refused instead.
func TestAQueryThatCannotBeAccountedForIsRefused(t *testing.T) {
	for _, c := range []struct {
		name  string
		query string
		says  string
	}{
		{"an unclosed bracket", `MATCH DISTANCE("alpha" WITHIN 0.4`, "never closed"},
		{"a stray closing bracket", `MATCH DISTANCE("alpha")) WITHIN 0.4`, "nothing to close"},
		{"an unterminated phrase", `MATCH DISTANCE("alpha) WITHIN 0.4`, "never closed"},
		{"an escape we cannot read", `MATCH DISTANCE("alpha\nbeta") WITHIN 0.4`, "backslash"},
		// A forgotten bracket leaves the phrase outside any clause. Forwarding
		// it because it happens to sit at depth zero would send the words on a
		// query the broker was going to reject anyway.
		{"a phrase with no clause around it", `MATCH DISTANCE "payroll incident" WITHIN 0.4`, "inside no clause"},
		{"a phrase after the namespace selector ends", `MATCH DISTANCE("a") WITHIN 0.4 NAMESPACE "acme" LIMIT 5 "payroll incident"`, "inside no clause"},
	} {
		t.Run(c.name, func(t *testing.T) {
			b, e := &stubBroker{}, &stubEmbedder{}

			message := searchError(t, b, e, c.query)

			if !strings.Contains(message, c.says) {
				t.Errorf("expected the refusal to explain %q, got: %s", c.says, message)
			}
			if b.calls != 0 {
				t.Error("the query reached Noetive")
			}
			if e.calls != 0 {
				t.Error("an inference was spent on a query that could not be sent")
			}
		})
	}
}

// An escaped quote is the one backslash that appears in real phrases. It must
// be embedded as the phrase the agent meant, not as the source bytes.
func TestAnEscapedQuoteInAnAnchorIsEmbeddedAsWritten(t *testing.T) {
	b, e := &stubBroker{}, &stubEmbedder{}

	searchWith(t, b, e, `MATCH DISTANCE("the \"soft delete\" path") WITHIN 0.5`)

	if len(e.texts) != 1 || e.texts[0] != `the "soft delete" path` {
		t.Errorf("expected the unescaped phrase, got %q", e.texts)
	}
}

// The broker refuses a query carrying more floats than its parser will hold,
// and says so as a parse error the agent cannot act on. Turning that into an
// arithmetic sentence with a ceiling in it is the difference between "your
// query is wrong" and "use at most eight anchors".
func TestTooManyAnchorsIsRefusedWithTheCeilingNamed(t *testing.T) {
	b, e := &stubBroker{}, &stubEmbedder{}

	var clauses []string
	for _, phrase := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"} {
		clauses = append(clauses, `DISTANCE("`+phrase+`") WITHIN 0.4`)
	}
	query := "MATCH " + strings.Join(clauses, " AND ")

	_, err := embedding.Precomputed(b, e).Search(context.Background(), semantik.SearchRequest{
		Query: query, Namespace: "incidents", Model: model, Dimensions: 1024,
	})
	if err == nil {
		t.Fatal("expected nine anchors at 1024 dimensions to be refused")
	}
	if !strings.Contains(err.Error(), "at most 8 anchors") {
		t.Errorf("expected the message to name the ceiling, got: %v", err)
	}
	if e.calls != 0 {
		t.Error("an inference was spent on a query that could not be sent")
	}
	if b.calls != 0 {
		t.Error("the query reached Noetive")
	}
}

// A single vector larger than the parser accepts is refused for the same
// reason, before anything is spent on producing it.
func TestADimensionalityLargerThanAQueryCanCarryIsRefused(t *testing.T) {
	b, e := &stubBroker{}, &stubEmbedder{}

	_, err := embedding.Precomputed(b, e).Search(context.Background(), semantik.SearchRequest{
		Query: `MATCH DISTANCE("alpha") WITHIN 0.4`, Namespace: "incidents", Model: model, Dimensions: 5000,
	})
	if err == nil {
		t.Fatal("expected a 5000-dimensional anchor to be refused")
	}
	if !strings.Contains(err.Error(), "4096") {
		t.Errorf("expected the message to name the maximum, got: %v", err)
	}
	if e.calls != 0 {
		t.Error("an inference was spent on a query that could not be sent")
	}
}

// ---------------------------------------------------------------------------
// JSON wire format
// ---------------------------------------------------------------------------

// decodeQuery reads back what was sent, so the assertions are about the query
// the broker will see rather than about how this package renders JSON.
func decodeSent(t *testing.T, query string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(query), &out); err != nil {
		t.Fatalf("the query that was sent is not valid JSON: %v\n%s", err, query)
	}
	return out
}

func TestJSONAnchorsAreReplacedInEveryClauseKind(t *testing.T) {
	for _, c := range []struct {
		name  string
		query string
	}{
		{"distance", `{"match":{"distance":{"anchor":"payroll incident","within":0.5}}}`},
		{"direction with a bare phrase", `{"match":{"direction":{"toward":"customer frustration","cone":0.4}}}`},
		{"direction with a list", `{"match":{"direction":{"toward":["customer frustration","billing complaint"]}}}`},
		{"contrast", `{"match":{"contrast":{"attract":["enterprise"],"repel":["free tier"],"within":0.45}}}`},
		{"nested", `{"match":{"and":[{"not":{"distance":{"anchor":"alpha"}}},{"or":[{"distance":{"anchor":"beta"}},{"distance":{"anchor":"gamma"}}]}]}}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			b, e := &stubBroker{}, &stubEmbedder{}
			searchWith(t, b, e, c.query)

			sent := b.searchReq.Query
			for _, phrase := range e.texts {
				if strings.Contains(sent, phrase) {
					t.Fatalf("the phrase %q survived the rewrite: %s", phrase, sent)
				}
			}
			decodeSent(t, sent)
		})
	}
}

// A bare phrase in an anchor-list position must become a list holding one
// vector. A bare array of numbers there reads as a list whose first entry is a
// number rather than an anchor, and the query stops parsing.
func TestABarePhraseInAnAnchorListBecomesANestedVector(t *testing.T) {
	b, e := &stubBroker{}, &stubEmbedder{}

	searchWith(t, b, e, `{"match":{"direction":{"toward":"customer frustration"}}}`)

	sent := decodeSent(t, b.searchReq.Query)
	direction := sent["match"].(map[string]any)["direction"].(map[string]any)
	toward, ok := direction["toward"].([]any)
	if !ok {
		t.Fatalf("expected toward to be a list, got %T", direction["toward"])
	}
	if len(toward) != 1 {
		t.Fatalf("expected one anchor, got %d", len(toward))
	}
	if _, ok := toward[0].([]any); !ok {
		t.Fatalf("expected the anchor to be a vector, got %T", toward[0])
	}
}

// Numbers this server does not touch must come back as the agent wrote them.
// A threshold re-rendered as 0.5000000001 changes what matches.
func TestJSONNumbersSurviveTheRoundTrip(t *testing.T) {
	b, e := &stubBroker{}, &stubEmbedder{}

	searchWith(t, b, e, `{"match":{"distance":{"anchor":"alpha","within":0.45,"top_k":7}},"window":"P7D","limit":20}`)

	sent := b.searchReq.Query
	for _, want := range []string{`"within":0.45`, `"top_k":7`, `"window":"P7D"`, `"limit":20`} {
		if !strings.Contains(sent, want) {
			t.Errorf("expected %s to survive, got %s", want, sent)
		}
	}
}

// A clause this server does not know may carry text in a field it has never
// heard of. There is no way to promise the query is clean, so it is refused
// rather than sent on the assumption that it is.
//
// The nested cases matter as much as the plain ones: a refusal that is noticed
// at the top level and dropped on the way out of a recursion is a leak that
// only shows up on the queries agents actually write.
func TestAnUnknownClauseOrFieldIsRefused(t *testing.T) {
	for _, c := range []struct {
		name  string
		query string
		says  string
	}{
		{"an unknown clause", `{"match":{"trajectory":{"through":["alpha"]}}}`, "does not recognise"},
		{"an unknown field", `{"match":{"distance":{"anchor":"alpha","weight":2}}}`, "does not recognise"},
		{"an unknown clause inside an and", `{"match":{"and":[{"distance":{"anchor":"alpha"}},{"resonance":{"with":"beta"}}]}}`, "does not recognise"},
		{"an unknown clause inside an or", `{"match":{"or":[{"distance":{"anchor":"alpha"}},{"resonance":{"with":"beta"}}]}}`, "does not recognise"},
		{"an unknown clause under a not", `{"match":{"not":{"resonance":{"with":"beta"}}}}`, "does not recognise"},
		{"an unknown field on a contrast", `{"match":{"contrast":{"attract":["alpha"],"weight":2}}}`, "does not recognise"},
		{"an unusable entry in attract", `{"match":{"contrast":{"attract":[7]}}}`, "attract"},
		{"an unusable entry in repel", `{"match":{"contrast":{"attract":["alpha"],"repel":[7]}}}`, "repel"},
		{"an unusable anchor", `{"match":{"distance":{"anchor":7}}}`, "anchor"},
		{"an unusable toward", `{"match":{"direction":{"toward":7}}}`, "toward"},
		{"an and that is not a list", `{"match":{"and":{"distance":{"anchor":"alpha"}}}}`, "list of expressions"},
		{"a clause that is not an object", `{"match":{"distance":"alpha"}}`, "object"},
		{"an expression naming two things", `{"match":{"distance":{"anchor":"alpha"},"direction":{"toward":"beta"}}}`, "exactly one"},
		{"no match clause at all", `{"limit":5}`, "no match clause"},
		{"a match that is not an expression", `{"match":"alpha"}`, "object"},
		{"an and entry that is not an expression", `{"match":{"and":["alpha","beta"]}}`, "object"},
		{"not readable as JSON", `{"match":`, "not readable as JSON"},
		{"two values", `{"match":{"distance":{"anchor":"alpha"}}} {}`, "more than one"},
	} {
		t.Run(c.name, func(t *testing.T) {
			b, e := &stubBroker{}, &stubEmbedder{}

			message := searchError(t, b, e, c.query)

			if !strings.Contains(message, c.says) {
				t.Errorf("expected the refusal to mention %q, got: %s", c.says, message)
			}
			if b.calls != 0 {
				t.Error("the query reached Noetive")
			}
		})
	}
}

// Blanking has to understand an escaped quote for the same reason the rewrite
// does. Reading \" as the end of the literal would put the rest of the phrase
// outside the quotes, where nothing blanks it — and the words would go anyway.
func TestBlankingUnderstandsAnEscapedQuote(t *testing.T) {
	b, e := &stubBroker{}, &stubEmbedder{}

	if _, err := embedding.Precomputed(b, e).Lint(context.Background(), semantik.LintRequest{
		Query: `MATCH DISTANCE("the \"soft delete\" path") WITHIN 0.5`,
	}); err != nil {
		t.Fatalf("lint: %v", err)
	}

	for _, leaked := range []string{"soft", "delete", "path"} {
		if strings.Contains(b.lintReq.Query, leaked) {
			t.Fatalf("the phrase leaked through blanking: %s", b.lintReq.Query)
		}
	}
	if want := `MATCH DISTANCE("x") WITHIN 0.5`; b.lintReq.Query != want {
		t.Errorf("expected %q, got %q", want, b.lintReq.Query)
	}
}

// A query with nothing in it must not be read as JSON, or the JSON path reports
// a decode failure where the text path would have said what was actually wrong.
func TestAWhitespaceOnlyQueryIsNotTreatedAsJSON(t *testing.T) {
	b, e := &stubBroker{}, &stubEmbedder{}

	searchWith(t, b, e, "   \t\n ")

	if b.searchReq.Query != "   \t\n " {
		t.Errorf("expected the query to arrive unchanged, got %q", b.searchReq.Query)
	}
}

// Lint reaches the JSON path too, and must strip it the same way.
func TestLintBlanksAJSONQuery(t *testing.T) {
	b, e := &stubBroker{}, &stubEmbedder{}

	if _, err := embedding.Precomputed(b, e).Lint(context.Background(), semantik.LintRequest{
		Query: `{"match":{"distance":{"anchor":"payroll incident","within":0.5}}}`,
	}); err != nil {
		t.Fatalf("lint: %v", err)
	}

	if strings.Contains(b.lintReq.Query, "payroll") {
		t.Fatalf("the anchor text reached the lint endpoint: %s", b.lintReq.Query)
	}
	if !strings.Contains(b.lintReq.Query, `"within":0.5`) {
		t.Errorf("expected the rest of the query to survive, got %s", b.lintReq.Query)
	}
}
