package embedding

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/goccy/go-json"
	"github.com/noetive/noetive-sdk-go/semantik"
)

// maxQueryVectors bounds the total float32 values this server will put into one
// query, across every vector anchor in it.
//
// This is the limit the feature makes bite: a text anchor costs a handful of
// bytes, and the vector that replaces it costs `dimensions` floats, so a query
// that was nowhere near any ceiling as text can sit against one as vectors.
// Checking here, before an embed call is spent, is what turns an opaque
// upstream rejection into a sentence naming what the agent has to write under.
//
// It is a local bound, deliberately not published as a promise anywhere. The
// far side enforces its own and is free to move it, so this can only ever be
// conservative — if it drifts low, the cost is a query refused here that would
// have been accepted, which is visible and reported rather than silent.
const maxQueryVectors = 8192

// blankAnchor is what an anchor's text becomes on the way to the lint endpoint.
// A single character keeps the query grammatical while carrying nothing.
const blankAnchor = "x"

var (
	errUnterminatedLiteral = errors.New("a quoted anchor is never closed")
	errNotAnObject         = errors.New("expected a JSON object")
)

// rewrite returns query with every text anchor replaced by the vector it stands
// for, so the phrases an agent wrote never reach Noetive.
//
// The syntax is chosen by the first significant byte: SemQL text never begins
// with '{', so the discriminator is exact rather than a guess. A query with no
// text anchors is returned byte-for-byte and costs no embed call — one nobody
// changed should reach the broker exactly as the agent wrote it, so that an
// error from the far side quotes something they recognise.
func rewrite(ctx context.Context, e Embedder, model string, dimensions uint16, query string) (string, error) {
	if jsonQuery(query) {
		return rewriteJSON(ctx, e, model, dimensions, query)
	}
	return rewriteText(ctx, e, model, dimensions, query)
}

// blank returns query with every anchor's text replaced by a placeholder, for
// the one call that is about checking a query rather than running it.
//
// It is deliberately more forgiving than rewrite. A half-written query is the
// normal case for lint — that is what completions are for — so an unterminated
// quote or an unclosed bracket is worked with rather than refused.
func blank(query string) (string, error) {
	if jsonQuery(query) {
		return blankJSON(query)
	}
	return blankText(query), nil
}

// jsonQuery reports whether query is written in the JSON wire format.
func jsonQuery(query string) bool {
	for i := 0; i < len(query); i++ {
		switch query[i] {
		case ' ', '\t', '\r', '\n':
		default:
			return query[i] == '{'
		}
	}
	return false
}

// phrases collects the anchor texts of one query: the distinct set to embed,
// and which of them each occurrence wants.
//
// Deduplication is by exact bytes and by linear scan. A query carries at most a
// few dozen anchors, so the scan is cheaper than the map allocation it would
// replace, and it costs nothing on the common query with one anchor.
//
// Field ordering: slices (24 B each).
type phrases struct {
	distinct []string
	slots    []int
}

func (p *phrases) add(text string) {
	for i, seen := range p.distinct {
		if seen == text {
			p.slots = append(p.slots, i)
			return
		}
	}
	p.distinct = append(p.distinct, text)
	p.slots = append(p.slots, len(p.distinct)-1)
}

// vectors embeds the distinct phrases in one call and returns one vector per
// occurrence, in the order the occurrences were collected.
//
// The budget is checked first, so a query that could never be sent does not
// cost an inference. It counts occurrences rather than distinct phrases:
// deduplication saves the embedder work, but each occurrence still becomes its
// own literal in the query and the broker counts every one.
func (p *phrases) vectors(ctx context.Context, e Embedder, model string, dimensions uint16) ([][]float32, error) {
	if dimensions == 0 {
		return nil, errors.New("no dimensionality was named")
	}
	if int(dimensions) > semantik.MaxVectorDim {
		return nil, fmt.Errorf("a %d-dimensional vector cannot appear in a query; the maximum is %d", dimensions, semantik.MaxVectorDim)
	}
	if total := len(p.slots) * int(dimensions); total > maxQueryVectors {
		return nil, fmt.Errorf(
			"this query has %d anchors at %d dimensions, which is %d floats; a query may carry at most %d, so use at most %d anchors at this dimensionality",
			len(p.slots), dimensions, total, maxQueryVectors, maxQueryVectors/int(dimensions),
		)
	}

	embedded, err := e.Embed(ctx, model, dimensions, p.distinct)
	if err != nil {
		return nil, err
	}
	if len(embedded) != len(p.distinct) {
		return nil, fmt.Errorf("asked for %d embeddings and got %d", len(p.distinct), len(embedded))
	}

	out := make([][]float32, len(p.slots))
	for i, slot := range p.slots {
		out[i] = embedded[slot]
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// SemQL text syntax
// ---------------------------------------------------------------------------

// span is one quoted anchor in a text query: the phrase it carries, and where
// its literal sits in the source, both quotes included.
//
// Field ordering: string (16 B) > ints (8 B each).
type span struct {
	text       string
	start, end int
}

// textAnchors reports every anchor in a SemQL text query.
//
// The rule comes from the grammar in tools/prompts/semql/grammar.md, and it is
// the whole algorithm: the only quoted strings a query can contain are anchors,
// which live inside DISTANCE(…), DIRECTION(…) and CONTRAST(…), and namespace
// refs, which follow NAMESPACE and are never parenthesised. A grouping paren
// only ever wraps an expression, which contains clauses, which bring their own
// parens. So a quoted string at bracket depth of one or more is an anchor.
//
// A quoted string at depth zero is a namespace ref, but only where a namespace
// ref can appear. Anywhere else it is a phrase in a query that does not parse —
// a forgotten bracket puts an anchor there — and it is refused rather than
// forwarded. Treating every depth-zero string as a name would send exactly the
// words this package exists to keep, on a query the broker was going to reject
// anyway.
//
// Anything else the grammar does not account for is refused for the same
// reason. A query we did not fully understand may hold text we did not see, and
// this function's answer is what the caller relies on to claim none is left.
func textAnchors(query string) ([]span, error) {
	var spans []span

	depth := 0
	// Whether the scan has passed NAMESPACE and not yet reached a clause that
	// ends the selector. Only inside it is a bare quoted string a name.
	naming := false

	for i := 0; i < len(query); {
		switch query[i] {
		case '(':
			depth++
			i++
		case ')':
			depth--
			if depth < 0 {
				return nil, fmt.Errorf("there is a closing bracket with nothing to close at byte %d", i)
			}
			i++
		case '"':
			text, next, err := readLiteral(query, i)
			if err != nil {
				return nil, err
			}
			switch {
			case depth > 0:
				spans = append(spans, span{text: text, start: i, end: next})
			case !naming:
				return nil, fmt.Errorf("there is a phrase at byte %d that is inside no clause and is not a namespace name; check the query with noetive_lint", i)
			}
			i = next
		default:
			if depth == 0 {
				switch {
				case keywordAt(query, i, "NAMESPACE"):
					naming = true
				case keywordAt(query, i, "WINDOW"), keywordAt(query, i, "LIMIT"):
					naming = false
				}
			}
			i++
		}
	}
	if depth != 0 {
		return nil, errors.New("a bracket is opened and never closed")
	}

	return spans, nil
}

// keywordAt reports whether keyword stands alone at query[i].
//
// SemQL's reserved words are case-insensitive, and the boundary check is what
// stops a clause named in passing — or an identifier that merely starts with
// one — being read as the keyword itself.
func keywordAt(query string, i int, keyword string) bool {
	end := i + len(keyword)
	if end > len(query) || !strings.EqualFold(query[i:end], keyword) {
		return false
	}
	if i > 0 && wordByte(query[i-1]) {
		return false
	}
	return end == len(query) || !wordByte(query[end])
}

func wordByte(b byte) bool {
	return b == '_' ||
		(b >= '0' && b <= '9') ||
		(b >= 'a' && b <= 'z') ||
		(b >= 'A' && b <= 'Z')
}

// readLiteral reads the quoted string starting at query[i] and returns its
// content and the offset just past the closing quote.
func readLiteral(query string, i int) (string, int, error) {
	for j := i + 1; j < len(query); j++ {
		switch query[j] {
		case '"':
			return query[i+1 : j], j + 1, nil
		case '\\':
			return readEscapedLiteral(query, i)
		}
	}
	return "", 0, errUnterminatedLiteral
}

// readEscapedLiteral is the slow path, taken only when a backslash appears.
//
// It understands \" and \\ and refuses anything else. The grammar does not
// publish the rest of the escape table, and a phrase is what gets embedded, so
// decoding one wrongly would anchor the query on something the agent did not
// write. A backslash in natural language is rare enough that refusing costs
// nothing and guessing could cost a wrong answer nobody notices.
func readEscapedLiteral(query string, i int) (string, int, error) {
	var b strings.Builder
	for j := i + 1; j < len(query); j++ {
		switch query[j] {
		case '"':
			return b.String(), j + 1, nil
		case '\\':
			if j+1 >= len(query) {
				return "", 0, errUnterminatedLiteral
			}
			switch query[j+1] {
			case '"', '\\':
				b.WriteByte(query[j+1])
				j++
			default:
				return "", 0, fmt.Errorf("the anchor at byte %d uses the escape \\%c, which this server cannot read; write the phrase without a backslash", i, query[j+1])
			}
		default:
			b.WriteByte(query[j])
		}
	}
	return "", 0, errUnterminatedLiteral
}

func rewriteText(ctx context.Context, e Embedder, model string, dimensions uint16, query string) (string, error) {
	spans, err := textAnchors(query)
	if err != nil {
		return "", err
	}
	if len(spans) == 0 {
		return query, nil
	}

	var p phrases
	for _, s := range spans {
		p.add(s.text)
	}
	vectors, err := p.vectors(ctx, e, model, dimensions)
	if err != nil {
		return "", err
	}

	out := spliceText(query, spans, vectors)

	// Prove it on the bytes that are about to be sent, not on the intention
	// that produced them. This is the check that makes the promise auditable:
	// whatever the scanner did or did not understand, an anchor still holding
	// text here means the query does not go.
	left, err := textAnchors(out)
	if err != nil {
		return "", fmt.Errorf("the rewritten query could not be re-read, so it was not sent: %w", err)
	}
	if len(left) > 0 {
		return "", fmt.Errorf("%d anchors still carry their text after rewriting, so the query was not sent", len(left))
	}

	return out, nil
}

// spliceText copies query, replacing each anchor literal with its vector.
//
// Every byte outside the spans survives unchanged — brackets, keywords,
// thresholds, the NAMESPACE clause and the agent's own spacing — so a rejection
// from the broker still quotes a query they recognise.
func spliceText(query string, spans []span, vectors [][]float32) string {
	// A float32 written with the shortest round-tripping decimal runs to about
	// fourteen bytes with its separator, which sizes the buffer in one go.
	out := make([]byte, 0, len(query)+len(spans)*(len(vectors[0])*14+2))

	prev := 0
	for i, s := range spans {
		out = append(out, query[prev:s.start]...)
		out = appendVector(out, vectors[i])
		prev = s.end
	}

	return string(append(out, query[prev:]...))
}

// appendVector writes a SemQL vector literal.
//
// Fixed-point rather than scientific notation: the grammar publishes `number`
// without saying whether an exponent is admitted, and 'f' sidesteps the
// question entirely at the cost of a few bytes on very small values.
func appendVector(dst []byte, vector []float32) []byte {
	dst = append(dst, '[')
	for i, v := range vector {
		if i > 0 {
			dst = append(dst, ',')
		}
		dst = strconv.AppendFloat(dst, float64(v), 'f', -1, 32)
	}
	return append(dst, ']')
}

// blankText replaces the contents of every quoted string in a text query.
//
// Every one, not only those at depth: lint is asked about queries that are
// still being typed, where bracket depth cannot be trusted, and a namespace
// name checked as "x" costs nothing. Working from the quotes alone is what
// makes it impossible to walk past a literal on a query this server could not
// otherwise make sense of.
func blankText(query string) string {
	out := make([]byte, 0, len(query))

	for i := 0; i < len(query); {
		if query[i] != '"' {
			out = append(out, query[i])
			i++
			continue
		}

		j := i + 1
		for j < len(query) && query[j] != '"' {
			if query[j] == '\\' {
				j++
			}
			j++
		}

		out = append(out, '"')
		out = append(out, blankAnchor...)
		if j < len(query) {
			// The literal was closed, so close the replacement. An unterminated
			// one is left unterminated: that is the diagnostic the agent needs.
			out = append(out, '"')
			j++
		}
		i = j
	}

	return string(out)
}

// ---------------------------------------------------------------------------
// SemQL JSON wire format
// ---------------------------------------------------------------------------

// jsonSite is one anchor position in a decoded query that currently holds text,
// with the two edits that can be made to it.
//
// Both are closures because a position may be a clause field or an element of
// an anchor list, and only the walk that found it knows which.
//
// Field ordering: funcs (8 B each) before the string (16 B), which groups the
// pointer words at the front.
type jsonSite struct {
	replace func(vector []float32)
	blank   func()
	text    string
}

// decodeQuery reads a JSON wire-format query.
//
// UseNumber keeps every number we do not touch as the literal the agent wrote,
// so a threshold does not come back re-rendered.
func decodeQuery(query string) (map[string]any, error) {
	dec := json.NewDecoder(strings.NewReader(query))
	dec.UseNumber()

	var root any
	if err := dec.Decode(&root); err != nil {
		return nil, fmt.Errorf("this query is not readable as JSON: %w", err)
	}
	if dec.More() {
		return nil, errors.New("this query has more than one JSON value in it")
	}

	obj, ok := root.(map[string]any)
	if !ok {
		return nil, errNotAnObject
	}
	return obj, nil
}

// jsonAnchors reports every anchor position in a decoded query that holds text.
//
// A clause this server does not recognise is refused rather than walked past.
// The point of the walk is to be able to say no text is left, and there is no
// way to say that about a shape whose fields are unknown.
func jsonAnchors(root map[string]any) ([]jsonSite, error) {
	match, ok := root["match"]
	if !ok {
		return nil, errors.New("this query has no match clause")
	}

	var sites []jsonSite
	if err := walkExpression(match, &sites); err != nil {
		return nil, err
	}
	return sites, nil
}

func walkExpression(node any, sites *[]jsonSite) error {
	obj, ok := node.(map[string]any)
	if !ok {
		return errNotAnObject
	}
	if len(obj) != 1 {
		return fmt.Errorf("an expression names exactly one of and, or, not, distance, direction or contrast; this one names %d things", len(obj))
	}

	for key, child := range obj {
		switch key {
		case "and", "or":
			list, ok := child.([]any)
			if !ok {
				return fmt.Errorf("%q takes a list of expressions", key)
			}
			for _, c := range list {
				if err := walkExpression(c, sites); err != nil {
					return err
				}
			}
			return nil

		case "not":
			return walkExpression(child, sites)

		case "distance":
			clause, err := clauseOf(child, key, "anchor", "within", "top_k", "metric")
			if err != nil {
				return err
			}
			// A bare float array is a valid anchor here, so the vector goes in
			// as-is. This is the one position where that is true.
			return singleAnchor(clause, "anchor", sites)

		case "direction":
			clause, err := clauseOf(child, key, "toward", "cone")
			if err != nil {
				return err
			}
			return anchorList(clause, "toward", sites)

		case "contrast":
			clause, err := clauseOf(child, key, "attract", "repel", "within")
			if err != nil {
				return err
			}
			if err := anchorList(clause, "attract", sites); err != nil {
				return err
			}
			return anchorList(clause, "repel", sites)

		default:
			return fmt.Errorf("this server does not recognise the clause %q, so it cannot promise the query carries no text; write the query in the SemQL text syntax instead", key)
		}
	}

	return nil
}

// clauseOf checks a clause carries only fields this server knows about, for the
// same reason walkExpression refuses an unknown clause.
func clauseOf(node any, name string, allowed ...string) (map[string]any, error) {
	clause, ok := node.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%q takes an object", name)
	}
	for key := range clause {
		if !slices.Contains(allowed, key) {
			return nil, fmt.Errorf("this server does not recognise the field %q on a %s clause, so it cannot promise the query carries no text", key, name)
		}
	}
	return clause, nil
}

// singleAnchor records a clause field that holds one anchor.
func singleAnchor(clause map[string]any, key string, sites *[]jsonSite) error {
	value, ok := clause[key]
	if !ok {
		// A missing anchor is the broker's to report; it is not text, so it is
		// not this function's business.
		return nil
	}

	switch anchor := value.(type) {
	case string:
		*sites = append(*sites, jsonSite{
			text:    anchor,
			replace: func(vector []float32) { clause[key] = vector },
			blank:   func() { clause[key] = blankAnchor },
		})
		return nil
	case []any:
		// Already a vector the agent computed. Left exactly as written.
		return nil
	default:
		return fmt.Errorf("%q must be a phrase or a vector", key)
	}
}

// anchorList records a clause field that holds one anchor or a list of them.
//
// A bare phrase is replaced by a list holding one vector rather than by a bare
// vector. The wire format accepts a bare phrase in these positions but not a
// bare array of numbers — that reads as a list whose first element is a number
// rather than an anchor — so the nesting is what keeps the query parseable.
func anchorList(clause map[string]any, key string, sites *[]jsonSite) error {
	value, ok := clause[key]
	if !ok {
		return nil
	}

	switch anchors := value.(type) {
	case string:
		*sites = append(*sites, jsonSite{
			text:    anchors,
			replace: func(vector []float32) { clause[key] = [][]float32{vector} },
			blank:   func() { clause[key] = blankAnchor },
		})
		return nil

	case []any:
		for i, element := range anchors {
			switch anchor := element.(type) {
			case string:
				*sites = append(*sites, jsonSite{
					text:    anchor,
					replace: func(vector []float32) { anchors[i] = vector },
					blank:   func() { anchors[i] = blankAnchor },
				})
			case []any:
				// Already a vector.
			default:
				return fmt.Errorf("every entry of %q must be a phrase or a vector", key)
			}
		}
		return nil

	default:
		return fmt.Errorf("%q must be a phrase, a list of phrases, or a vector", key)
	}
}

func rewriteJSON(ctx context.Context, e Embedder, model string, dimensions uint16, query string) (string, error) {
	root, err := decodeQuery(query)
	if err != nil {
		return "", err
	}
	sites, err := jsonAnchors(root)
	if err != nil {
		return "", err
	}
	if len(sites) == 0 {
		return query, nil
	}

	var p phrases
	for _, s := range sites {
		p.add(s.text)
	}
	vectors, err := p.vectors(ctx, e, model, dimensions)
	if err != nil {
		return "", err
	}
	for i, s := range sites {
		s.replace(vectors[i])
	}

	out, err := json.Marshal(root)
	if err != nil {
		return "", err
	}

	// The same proof the text path takes, on the bytes about to be sent.
	if err := noTextLeft(out); err != nil {
		return "", err
	}

	return string(out), nil
}

// noTextLeft re-reads a rewritten query and refuses it if any anchor still
// holds a phrase.
func noTextLeft(out []byte) error {
	root, err := decodeQuery(string(out))
	if err != nil {
		return fmt.Errorf("the rewritten query could not be re-read, so it was not sent: %w", err)
	}
	sites, err := jsonAnchors(root)
	if err != nil {
		return fmt.Errorf("the rewritten query could not be re-read, so it was not sent: %w", err)
	}
	if len(sites) > 0 {
		return fmt.Errorf("%d anchors still carry their text after rewriting, so the query was not sent", len(sites))
	}
	return nil
}

func blankJSON(query string) (string, error) {
	root, err := decodeQuery(query)
	if err != nil {
		return "", err
	}
	sites, err := jsonAnchors(root)
	if err != nil {
		return "", err
	}
	for _, s := range sites {
		s.blank()
	}

	out, err := json.Marshal(root)
	if err != nil {
		return "", err
	}
	return string(out), nil
}
