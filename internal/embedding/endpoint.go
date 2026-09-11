package embedding

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/goccy/go-json"
	"github.com/noetive/noetive-sdk-go/semantik"

	"github.com/noetive/noetive-mcp/internal/mcpserver"
)

// Environment variables read by the command when it builds an Endpoint.
//
// There is deliberately no variable for the model or the dimensionality.
// Both already have exactly one name — NOETIVE_MODEL and NOETIVE_DIMENSIONS —
// and that name is sent to Noetive as the routing triple and to this endpoint
// as the request's model and dimensions. A second variable would be a second
// thing to get wrong, and getting it wrong is undetectable: an endpoint serving
// a different model returns right-length vectors in the wrong space, so every
// publish lands where nothing will find it and every query returns confident
// nonsense.
const (
	EnvURL = "NOETIVE_EMBEDDINGS_URL"
	EnvKey = "NOETIVE_EMBEDDINGS_KEY_SECRET"
)

// EnvNames lists every environment variable this package reads, in the order
// they are documented.
//
// Exported so the packaging emitter can check server.json against it rather
// than keeping a second list that drifts, exactly as targeting.EnvNames does.
func EnvNames() []string {
	return []string{EnvURL, EnvKey}
}

// responseHeaderTimeout bounds a stalled dial without constraining a model that
// is genuinely slow to load. The overall budget is the caller's context; this
// only stops a connection that produced no headers at all from holding the
// agent's turn open until the tool deadline.
const responseHeaderTimeout = 30 * time.Second

// Endpoint is an OpenAI-compatible /v1/embeddings service on a host the
// operator named. It is immutable after At returns and safe for concurrent use.
//
// The model is not a field. It arrives on every call from the routing triple,
// so there is nowhere for a second, disagreeing name to be stored.
//
// Field ordering: pointer (8 B) > strings (16 B each).
type Endpoint struct {
	http *http.Client
	// url is where the request goes, and is also what appears in an error. The
	// two can be the same string because At refuses a URL carrying userinfo
	// outright rather than stripping it, so there is no credential here to echo
	// back through a tool result into the model's context. The key lives in
	// authHeader, which is never rendered — see String.
	url        string
	authHeader string
}

// At builds the endpoint the operator configured, or reports why it could not.
//
// A blank baseURL returns (nil, nil): the operator asked for nothing, and the
// server embeds through Noetive exactly as it always has. This is the only
// place a nil *Endpoint is produced, and the caller branches on it immediately.
//
//	e, err := embedding.At(os.Getenv(embedding.EnvURL), os.Getenv(embedding.EnvKey))
func At(baseURL, key string) (*Endpoint, error) {
	raw := strings.TrimSpace(baseURL)
	if raw == "" {
		return nil, nil
	}

	// An editor that never expanded ${NOETIVE_EMBEDDINGS_URL} hands over the
	// literal text, which is a perfectly well-formed string that url.Parse
	// would happily reject with a message about the character '$'. Naming the
	// real fault sends the user to their editor's environment instead.
	if mcpserver.PlaceholderKey(raw) {
		return nil, fmt.Errorf("embedding: %s is the literal text %q, which means your editor did not substitute it; desktop launchers do not read your shell profile", EnvURL, raw)
	}

	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("embedding: %s=%q is not a URL: %w", EnvURL, raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("embedding: %s=%q must start with http:// or https://", EnvURL, raw)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("embedding: %s=%q names no host", EnvURL, raw)
	}
	// Each of these means something other than an embeddings endpoint was
	// pasted, most often a dashboard page. Accepting them would send the text
	// this feature exists to protect somewhere nobody intended.
	if u.User != nil {
		return nil, fmt.Errorf("embedding: %s must not carry credentials in the URL; put the key in %s", EnvURL, EnvKey)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("embedding: %s=%q must be a plain endpoint URL, with no query string or fragment", EnvURL, raw)
	}
	// Plaintext is allowed only to this machine. The whole point of embedding
	// here is that the text does not travel; a typo'd hostname over http would
	// send every message and every anchor across the network in the clear.
	//
	// The check is on the literal the operator wrote, never on a DNS lookup:
	// resolving here and trusting the answer is both a time-of-check race and
	// an invitation to DNS rebinding.
	if u.Scheme == "http" && !loopback(u.Hostname()) {
		return nil, fmt.Errorf("embedding: %s=%q sends text in the clear to a host that is not this machine; use https, or a loopback address", EnvURL, raw)
	}

	u.Path = resolvePath(u.Path)

	e := &Endpoint{http: refusingClient(), url: u.String()}

	if k := strings.TrimSpace(key); k != "" {
		if mcpserver.PlaceholderKey(k) {
			return nil, fmt.Errorf("embedding: %s is an unexpanded variable reference rather than a key; either export it in the environment your editor launches from, or leave it unset if your endpoint needs no key", EnvKey)
		}
		e.authHeader = "Bearer " + k
	}

	return e, nil
}

// resolvePath accepts the three spellings people actually write. Guessing wrong
// produces /v1/embeddings/v1/embeddings, which is a 404 that reads like the
// service is down rather than like a configuration typo.
func resolvePath(path string) string {
	p := strings.TrimRight(path, "/")
	switch {
	case strings.HasSuffix(p, "/v1/embeddings"):
		return p
	case strings.HasSuffix(p, "/v1"):
		return p + "/embeddings"
	default:
		return p + "/v1/embeddings"
	}
}

// loopback reports whether host is this machine, written literally.
func loopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	addr, err := netip.ParseAddr(host)
	return err == nil && addr.IsLoopback()
}

// refusingClient is the transport this package uses.
//
// Proxy is cleared: an ambient HTTPS_PROXY would route the very text this
// feature keeps off the network through a third party, silently. An operator
// who needs a proxy points EnvURL at it directly.
//
// Redirects are refused rather than followed. The SDK does this so a 3xx cannot
// capture the bearer; here the stakes are higher, because following one would
// forward the input text itself to whatever the Location header names.
func refusingClient() *http.Client {
	var t *http.Transport
	if base, ok := http.DefaultTransport.(*http.Transport); ok {
		t = base.Clone()
	} else {
		t = &http.Transport{}
	}
	t.Proxy = nil
	t.ResponseHeaderTimeout = responseHeaderTimeout

	return &http.Client{
		Transport:     t,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// String returns a redacted representation so that fmt.Sprintf("%v", e) cannot
// leak the endpoint's key into logs, panics or debugger output.
func (e *Endpoint) String() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("embedding.Endpoint{url:%q, key:REDACTED}", e.url)
}

// GoString returns a redacted Go-syntax representation for the %#v verb.
func (e *Endpoint) GoString() string {
	if e == nil {
		return "(*embedding.Endpoint)(nil)"
	}
	return fmt.Sprintf("&embedding.Endpoint{url:%q, key:REDACTED}", e.url)
}

// embedRequest is the body of POST /v1/embeddings.
//
// Field ordering: strings (16 B each) before the slice (24 B) before the uint16
// (2 B), which groups the pointer words at the front.
type embedRequest struct {
	Model string `json:"model"`
	// EncodingFormat is sent explicitly even though the API defaults to float.
	// Gateways exist that default to base64, and a base64 string decoded into
	// []float32 yields an empty vector rather than an error — a silently wrong
	// embedding is worse than a loud rejection.
	EncodingFormat string   `json:"encoding_format"`
	Input          []string `json:"input"`
	// Dimensions is what makes a Matryoshka model usable: qwen3-embedding:4b
	// is natively 2560, and a namespace provisioned at 1024 needs the endpoint
	// to truncate. It is best-effort — the guarantee is the length check on
	// the way back, not this field.
	Dimensions uint16 `json:"dimensions"`
}

type embedResponse struct {
	Data []embedDatum `json:"data"`
}

// embedDatum is one embedding. Index is authoritative for which input it
// belongs to; see Embed for why it is not treated as advisory.
//
// Field ordering: slice (24 B) > int (8 B).
type embedDatum struct {
	Embedding []float32 `json:"embedding"`
	Index     int       `json:"index"`
}

// Embed returns one vector per text, in the order the texts were given, each of
// exactly dimensions elements.
//
// model and dimensions come from the routing triple and are passed through
// unchanged. The vectors are returned exactly as the endpoint produced them:
// no normalisation, no scaling, no truncation. Any transformation here would
// silently change what matches what, so if a different vector is wanted it is
// the endpoint's job to produce it.
//
// It never returns a partial result. An input that cannot be embedded fails the
// whole call, because a query rewritten with four vectors and one phrase still
// carries the phrase.
func (e *Endpoint) Embed(ctx context.Context, model string, dimensions uint16, texts []string) ([][]float32, error) {
	if err := e.refuseBadInput(model, dimensions, texts); err != nil {
		return nil, err
	}

	body, err := json.Marshal(embedRequest{
		Input:          texts,
		Model:          model,
		EncodingFormat: "float",
		Dimensions:     dimensions,
	})
	if err != nil {
		return nil, &Error{Endpoint: e.url, Model: model, Message: err.Error()}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.url, bytes.NewReader(body))
	if err != nil {
		return nil, &Error{Endpoint: e.url, Model: model, Message: err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if e.authHeader != "" {
		req.Header.Set("Authorization", e.authHeader)
	}

	resp, err := e.http.Do(req)
	if err != nil {
		// The caller withdrawing is not a failure of the endpoint, and it is
		// reported unwrapped so broker.failure recognises it and says so.
		if errors.Is(err, context.Canceled) {
			return nil, err
		}
		// Everything else, including a deadline, is wrapped. *Error has no
		// Unwrap on purpose: letting a DeadlineExceeded through would make
		// broker.failure print "no response within 30s" and send the operator
		// to check Noetive when it was the model on this machine that stalled.
		return nil, &Error{Endpoint: e.url, Model: model, Message: err.Error()}
	}
	defer func() { _ = resp.Body.Close() }()

	// A float32 costs at most ~24 bytes of JSON, so this bounds a broken or
	// hostile endpoint without ever truncating a well-formed reply.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, int64(len(texts))*int64(dimensions)*24+4096))
	if err != nil {
		return nil, &Error{Endpoint: e.url, Model: model, Status: resp.StatusCode, Message: err.Error()}
	}

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, &Error{Endpoint: e.url, Model: model, Status: resp.StatusCode, Message: describeBody(raw)}
	}

	var decoded embedResponse
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, &Error{Endpoint: e.url, Model: model, Status: resp.StatusCode, Message: "the reply was not an embeddings response: " + err.Error()}
	}
	// decoded.Model is deliberately not compared against model. Servers echo
	// filesystem paths, digests, aliases and quantisation suffixes, so a
	// comparison would fail on correct setups and prove nothing on wrong ones.

	return e.scatter(model, dimensions, texts, decoded.Data)
}

// refuseBadInput rejects what the endpoint would reject, or worse would accept.
// Each of these fails before a request is sent, so a mistake costs nothing.
func (e *Endpoint) refuseBadInput(model string, dimensions uint16, texts []string) error {
	if model == "" {
		return errors.New("embedding: no model was named; the namespace's model is what the endpoint must be asked for")
	}
	if dimensions == 0 {
		return errors.New("embedding: no dimensionality was named")
	}
	if dimensions > semantik.MaxVectorDim {
		return fmt.Errorf("embedding: %d dimensions exceeds the maximum of %d", dimensions, semantik.MaxVectorDim)
	}
	if len(texts) == 0 {
		return errors.New("embedding: nothing to embed")
	}
	for i, t := range texts {
		if strings.TrimSpace(t) == "" {
			// Blank text produces an embedding of nothing, which then matches
			// arbitrarily. Refusing costs one call; accepting pollutes an index.
			return fmt.Errorf("embedding: input %d is blank", i)
		}
		if len(t) > semantik.MaxTextBytes {
			return fmt.Errorf("embedding: input %d is %d bytes, over the %d-byte maximum", i, len(t), semantik.MaxTextBytes)
		}
	}
	return nil
}

// scatter puts the endpoint's reply back into the order the texts were given.
//
// index is treated as authoritative rather than advisory, and there is no
// positional fallback. A server that omits the field decodes every element as
// zero, and without the duplicate check that would silently give every input
// the first vector — publishing one message under another's meaning,
// permanently, with nothing anywhere to notice it.
func (e *Endpoint) scatter(model string, dimensions uint16, texts []string, data []embedDatum) ([][]float32, error) {
	if len(data) != len(texts) {
		return nil, &Error{
			Endpoint: e.url, Model: model,
			Message: fmt.Sprintf("asked for %d embeddings and got %d", len(texts), len(data)),
		}
	}

	out := make([][]float32, len(texts))
	for _, d := range data {
		if d.Index < 0 || d.Index >= len(texts) {
			return nil, &Error{
				Endpoint: e.url, Model: model,
				Message: fmt.Sprintf("returned an embedding for input %d, but only %d were sent", d.Index, len(texts)),
			}
		}
		if out[d.Index] != nil {
			return nil, &Error{
				Endpoint: e.url, Model: model,
				Message: fmt.Sprintf("returned two embeddings for input %d, so which text each one belongs to is unknowable", d.Index),
			}
		}
		if len(d.Embedding) != int(dimensions) {
			return nil, &Error{
				Endpoint: e.url, Model: model,
				Message: fmt.Sprintf("returned a %d-dimensional vector for input %d, but the namespace is %d-dimensional; either the endpoint is serving a different model or %s does not match it", len(d.Embedding), d.Index, dimensions, "NOETIVE_DIMENSIONS"),
			}
		}
		for i, v := range d.Embedding {
			// A non-finite element cannot be published — the SDK rejects it —
			// and written into a query it would serialise as a bare NaN, which
			// is not valid JSON and not a valid SemQL number.
			if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
				return nil, &Error{
					Endpoint: e.url, Model: model,
					Message: fmt.Sprintf("returned a vector for input %d whose element %d is not a finite number", d.Index, i),
				}
			}
		}
		out[d.Index] = d.Embedding
	}

	for i := range out {
		if out[i] == nil {
			return nil, &Error{
				Endpoint: e.url, Model: model,
				Message: fmt.Sprintf("returned no embedding for input %d", i),
			}
		}
	}

	return out, nil
}

// maxQuotedBody is how much of an unrecognised error body is worth showing.
const maxQuotedBody = 512

// describeBody turns whatever an endpoint said about a failure into one line an
// agent can act on. The structured shape is tried first because that is what
// carries the sentence a human wrote; only the message is read, because
// OpenAI-compatible servers disagree on whether code is a string, a number or
// absent, and decoding a field we do not need is a needless way to fail.
func describeBody(raw []byte) string {
	var envelope struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &envelope); err == nil && envelope.Error.Message != "" {
		return clean(envelope.Error.Message)
	}
	if len(raw) > maxQuotedBody {
		raw = raw[:maxQuotedBody]
	}
	return clean(string(raw))
}

// clean collapses control characters so an HTML error page or a stray newline
// cannot break up the single line a tool result is read as.
func clean(s string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s))
}

// Error is a failure of the endpoint on this machine, kept distinct from a
// *semantik.Error so broker.failure renders it as what it is: something local
// went wrong, and nothing was sent to Noetive.
//
// It deliberately does not implement Unwrap. See Embed.
//
// Field ordering: strings (16 B each) > int (8 B).
type Error struct {
	Endpoint string
	Model    string
	Message  string
	Status   int
}

func (e *Error) Error() string {
	var b strings.Builder
	b.Grow(len(e.Endpoint) + len(e.Model) + len(e.Message) + 64)

	b.WriteString("the local embedder at ")
	b.WriteString(e.Endpoint)
	if e.Model != "" {
		// Naming the exact string we asked for is what makes a 404 diagnosable:
		// the endpoint has to answer to the name the namespace uses, and this
		// is the sentence that says which name that was.
		b.WriteString(" asked for model ")
		b.WriteString(strconv.Quote(e.Model))
	}
	if e.Status != 0 {
		b.WriteString(" and got ")
		b.WriteString(strconv.Itoa(e.Status))
	} else {
		b.WriteString(" and failed")
	}
	if e.Message != "" {
		b.WriteString(": ")
		b.WriteString(e.Message)
	}

	return b.String()
}
