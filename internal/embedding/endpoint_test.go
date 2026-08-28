package embedding_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/noetive/noetive-mcp/internal/embedding"
)

// captured is what the endpoint actually received, so the assertions are about
// the request on the wire rather than about how this package builds one.
type captured struct {
	body   map[string]any
	path   string
	method string
	auth   string
	seen   bool
}

// serving stands up a loopback endpoint and returns it alongside what it saw.
func serving(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) (*embedding.Endpoint, *captured) {
	t.Helper()

	got := &captured{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.seen = true
		got.method, got.path, got.auth = r.Method, r.URL.Path, r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &got.body)
		handler(w, r)
	}))
	t.Cleanup(srv.Close)

	e, err := embedding.At(srv.URL, "")
	if err != nil {
		t.Fatalf("At: %v", err)
	}
	return e, got
}

// answering replies with one vector per input, in order.
func answering(vectors ...[]float32) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		data := make([]map[string]any, 0, len(vectors))
		for i, v := range vectors {
			data = append(data, map[string]any{"index": i, "embedding": v})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}
}

func replying(body string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, body)
	}
}

// The request has to be one an OpenAI-compatible server recognises, and
// encoding_format has to be stated: a gateway that defaults to base64 would
// decode into an empty vector rather than failing, which is a wrong embedding
// nothing downstream would notice.
func TestTheRequestIsAWellFormedEmbeddingsCall(t *testing.T) {
	e, got := serving(t, answering([]float32{0.1, 0.2, 0.3}))

	if _, err := e.Embed(context.Background(), model, 3, []string{"alpha"}); err != nil {
		t.Fatalf("embed: %v", err)
	}

	if got.method != http.MethodPost {
		t.Errorf("expected POST, got %s", got.method)
	}
	if got.path != "/v1/embeddings" {
		t.Errorf("expected /v1/embeddings, got %s", got.path)
	}
	if got.body["model"] != model {
		t.Errorf("expected the namespace's model %q on the wire, got %v", model, got.body["model"])
	}
	if got.body["encoding_format"] != "float" {
		t.Errorf("expected encoding_format float, got %v", got.body["encoding_format"])
	}
	if got.body["dimensions"] != float64(3) {
		t.Errorf("expected dimensions 3, got %v", got.body["dimensions"])
	}
	// Always an array, even for one text: that is the batching mechanism, and a
	// server that branches on scalar-versus-array never has to.
	input, ok := got.body["input"].([]any)
	if !ok || len(input) != 1 || input[0] != "alpha" {
		t.Errorf("expected input to be a one-element array, got %v", got.body["input"])
	}
}

// An endpoint on this machine usually needs no key, and sending an empty bearer
// is the kind of thing a strict server rejects for no reason.
func TestTheBearerIsSentOnlyWhenAKeyIsConfigured(t *testing.T) {
	e, got := serving(t, answering([]float32{0, 0, 0}))
	if _, err := e.Embed(context.Background(), model, 3, []string{"alpha"}); err != nil {
		t.Fatalf("embed: %v", err)
	}
	if got.auth != "" {
		t.Errorf("expected no Authorization header, got %q", got.auth)
	}
}

func TestTheBearerIsSentWhenAKeyIsConfigured(t *testing.T) {
	got := &captured{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.auth = r.Header.Get("Authorization")
		answering([]float32{0, 0, 0})(w, r)
	}))
	defer srv.Close()

	e, err := embedding.At(srv.URL, "sk-local-secret")
	if err != nil {
		t.Fatalf("At: %v", err)
	}
	if _, err := e.Embed(context.Background(), model, 3, []string{"alpha"}); err != nil {
		t.Fatalf("embed: %v", err)
	}
	if got.auth != "Bearer sk-local-secret" {
		t.Errorf("expected a bearer header, got %q", got.auth)
	}
}

// The highest-consequence check in the package. The API does not promise order,
// and a reply read positionally would publish one message under another's
// meaning — permanently, and with every result still looking plausible.
func TestAReplyOutOfOrderIsPutBackInOrderByItsIndex(t *testing.T) {
	e, _ := serving(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{
			{"index": 2, "embedding": []float32{3, 0, 0}},
			{"index": 0, "embedding": []float32{1, 0, 0}},
			{"index": 1, "embedding": []float32{2, 0, 0}},
		}})
	})

	vectors, err := e.Embed(context.Background(), model, 3, []string{"first", "second", "third"})
	if err != nil {
		t.Fatalf("embed: %v", err)
	}
	for i, v := range vectors {
		if v[0] != float32(i+1) {
			t.Errorf("input %d got the vector for input %v", i, v[0]-1)
		}
	}
}

// A server that omits index decodes every entry as zero. Without this check
// that reads as "they all belong to the first input", which is the same silent
// mis-assignment the ordering check exists to prevent.
func TestAReplyThatRepeatsAnIndexIsRefused(t *testing.T) {
	e, _ := serving(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{
			{"embedding": []float32{1, 0, 0}},
			{"embedding": []float32{2, 0, 0}},
		}})
	})

	_, err := e.Embed(context.Background(), model, 3, []string{"first", "second"})
	if err == nil {
		t.Fatal("expected a reply with no usable index to be refused")
	}
	if !strings.Contains(err.Error(), "two embeddings") {
		t.Errorf("expected the message to explain the collision, got: %v", err)
	}
}

// One past the end is the interesting case, not seven past it: an off-by-one
// here would index a slice out of range and take the editor's whole MCP session
// down with the panic.
func TestAReplyWithAnIndexNobodyAskedForIsRefused(t *testing.T) {
	for _, index := range []int{-1, 1, 7} {
		t.Run(fmt.Sprintf("index %d", index), func(t *testing.T) {
			e, _ := serving(t, func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{
					{"index": index, "embedding": []float32{1, 0, 0}},
				}})
			})

			_, err := e.Embed(context.Background(), model, 3, []string{"only one"})
			if err == nil {
				t.Fatal("expected an out-of-range index to be refused")
			}
			if !strings.Contains(err.Error(), "only 1 were sent") {
				t.Errorf("expected the message to say how many were sent, got: %v", err)
			}
		})
	}
}

// Never a partial result: a caller that got three vectors for four texts would
// have no way to tell which text went unembedded.
func TestAReplyWithTheWrongNumberOfEmbeddingsIsRefused(t *testing.T) {
	for _, c := range []struct {
		name    string
		vectors [][]float32
	}{
		{"too few", [][]float32{{1, 0, 0}}},
		{"too many", [][]float32{{1, 0, 0}, {2, 0, 0}, {3, 0, 0}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			e, _ := serving(t, answering(c.vectors...))

			_, err := e.Embed(context.Background(), model, 3, []string{"first", "second"})
			if err == nil {
				t.Fatal("expected a mismatched batch to be refused")
			}
			if !strings.Contains(err.Error(), "asked for 2") {
				t.Errorf("expected the message to name both counts, got: %v", err)
			}
		})
	}
}

// The check that catches the common misconfiguration: an endpoint serving a
// model at its native size against a namespace provisioned at another. It is
// also the only half of "is this the same vector space" that is checkable.
func TestAVectorOfTheWrongLengthIsRefusedNamingBothNumbers(t *testing.T) {
	e, _ := serving(t, answering([]float32{1, 2, 3, 4, 5}))

	_, err := e.Embed(context.Background(), model, 3, []string{"alpha"})
	if err == nil {
		t.Fatal("expected a five-element vector to be refused for a three-dimensional namespace")
	}
	message := err.Error()
	if !strings.Contains(message, "5-dimensional") || !strings.Contains(message, "3-dimensional") {
		t.Errorf("expected both sizes in the message, got: %s", message)
	}
	if !strings.Contains(message, "NOETIVE_DIMENSIONS") {
		t.Errorf("expected the message to name the setting to change, got: %s", message)
	}
}

// A non-finite element cannot be published, and written into a query it would
// serialise as a bare NaN or +Inf — which is neither valid JSON nor a valid
// SemQL number, so the query would fail to parse with no clue as to why.
// Both signs, because a check that only looks for one leaves the other to be
// written into a query as a bare -Inf.
func TestANonFiniteElementIsRefused(t *testing.T) {
	for _, body := range []string{
		`{"data":[{"index":0,"embedding":[1e39,0,0]}]}`,
		`{"data":[{"index":0,"embedding":[-1e39,0,0]}]}`,
		`{"data":[{"index":0,"embedding":[0,0,-1e39]}]}`,
	} {
		e, _ := serving(t, replying(body))

		_, err := e.Embed(context.Background(), model, 3, []string{"alpha"})
		if err == nil {
			t.Fatalf("expected %s to be refused", body)
		}
		if !strings.Contains(err.Error(), "finite") {
			t.Errorf("expected the message to say what was wrong, got: %v", err)
		}
	}
}

// A structured envelope with nothing in it says less than the body did. Falling
// through to the raw text is what keeps a badly-behaved endpoint diagnosable.
func TestAnEmptyErrorMessageFallsBackToTheBody(t *testing.T) {
	e, _ := serving(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":{"message":"","type":"overloaded"}}`)
	})

	_, err := e.Embed(context.Background(), model, 3, []string{"alpha"})
	if err == nil {
		t.Fatal("expected a 500 to be reported")
	}
	if !strings.Contains(err.Error(), "overloaded") {
		t.Errorf("expected the raw body when the envelope carried no message, got: %v", err)
	}
}

// A 2xx that is not 200 is still success. Treating 299 as a failure would
// refuse an answer that was perfectly good.
func TestAnUnusualSuccessStatusIsStillSuccess(t *testing.T) {
	e, _ := serving(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(299)
		answering([]float32{1, 0, 0})(w, r)
	})

	if _, err := e.Embed(context.Background(), model, 3, []string{"alpha"}); err != nil {
		t.Errorf("expected a 299 to be accepted, got %v", err)
	}
}

// A reply that is not an embeddings response at all must say so, rather than
// surfacing a decoder's phrasing that names no service.
func TestAReplyThatIsNotAnEmbeddingsResponseSaysSo(t *testing.T) {
	e, _ := serving(t, replying(`{"data":"not a list"}`))

	_, err := e.Embed(context.Background(), model, 3, []string{"alpha"})
	if err == nil {
		t.Fatal("expected an unusable reply to be refused")
	}
	if !strings.Contains(err.Error(), "not an embeddings response") {
		t.Errorf("expected the message to name the problem, got: %v", err)
	}
}

// What the endpoint said about the failure is the only thing that tells an
// operator which of the two services to go and look at.
func TestAnErrorBodyIsQuotedWithItsStatusAndTheModelAsked(t *testing.T) {
	e, _ := serving(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":{"message":"model not found"}}`)
	})

	_, err := e.Embed(context.Background(), model, 3, []string{"alpha"})
	if err == nil {
		t.Fatal("expected a 404 to be reported")
	}
	message := err.Error()
	for _, want := range []string{"404", "model not found", model} {
		if !strings.Contains(message, want) {
			t.Errorf("expected %q in the message, got: %s", want, message)
		}
	}
	// The sentence the endpoint wrote, not the envelope around it. Handing back
	// raw JSON buries the one readable thing in it.
	if strings.Contains(message, `{"error"`) {
		t.Errorf("expected the message rather than the whole body, got: %s", message)
	}
}

// A body this large is either a broken endpoint or a proxy. Quoting all of it
// would push everything else out of the agent's context.
func TestAnEnormousErrorBodyIsTruncated(t *testing.T) {
	e, _ := serving(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, strings.Repeat("x", 40_000))
	})

	_, err := e.Embed(context.Background(), model, 3, []string{"alpha"})
	if err == nil {
		t.Fatal("expected a 500 to be reported")
	}
	if len(err.Error()) > 2000 {
		t.Errorf("expected the body to be truncated, got %d bytes", len(err.Error()))
	}
}

// Formatting a nil endpoint happens in a panic trace, which is exactly when a
// second panic from inside the formatter is least welcome.
func TestFormattingANilEndpointSaysSo(t *testing.T) {
	var e *embedding.Endpoint

	for _, rendered := range []string{fmt.Sprintf("%v", e), fmt.Sprintf("%#v", e)} {
		if !strings.Contains(rendered, "nil") {
			t.Errorf("expected a nil endpoint to render as nil, got %q", rendered)
		}
	}
}

// A proxy or a misrouted URL answers with HTML. Reporting that as a JSON decode
// failure would send the operator looking at the wrong thing.
func TestAnUnstructuredErrorBodyIsStillReportedWithItsStatus(t *testing.T) {
	e, _ := serving(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, "<html><body>\n502 Bad Gateway\n</body></html>")
	})

	_, err := e.Embed(context.Background(), model, 3, []string{"alpha"})
	if err == nil {
		t.Fatal("expected a 502 to be reported")
	}
	if !strings.Contains(err.Error(), "502") {
		t.Errorf("expected the status in the message, got: %v", err)
	}
	if strings.Contains(err.Error(), "\n") {
		t.Errorf("the message spans lines, which a tool result reads as one: %q", err.Error())
	}
}

// Following a redirect would forward the input text — the very thing this
// feature keeps off the network — to whatever the Location header names.
func TestARedirectIsNotFollowed(t *testing.T) {
	elsewhere := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("the request was forwarded to the redirect target")
	}))
	defer elsewhere.Close()

	e, _ := serving(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Redirect(w, &http.Request{}, elsewhere.URL+"/v1/embeddings", http.StatusFound)
	})

	if _, err := e.Embed(context.Background(), model, 3, []string{"alpha"}); err == nil {
		t.Fatal("expected a redirect to be reported rather than chased")
	}
}

// A local stall must not read as Noetive failing to answer. broker.failure
// branches on the deadline, so letting one through would print "no response
// within 30s" against a service that was never asked.
func TestAnEndpointThatIsNotThereDoesNotLookLikeADeadline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()

	e, err := embedding.At(url, "")
	if err != nil {
		t.Fatalf("At: %v", err)
	}

	_, err = e.Embed(context.Background(), model, 3, []string{"alpha"})
	if err == nil {
		t.Fatal("expected a closed endpoint to fail")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Error("a connection failure was reported as a deadline")
	}
	if !strings.Contains(err.Error(), "local embedder") {
		t.Errorf("expected the message to name the local endpoint, got: %v", err)
	}
}

// The caller withdrawing is not a failure of anything, and the tool layer says
// so — but only if the cancellation survives to be recognised.
func TestACancelledCallIsReportedAsACancellation(t *testing.T) {
	e, _ := serving(t, func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})

	ctx, cancel := context.WithCancel(context.Background())
	go cancel()

	_, err := e.Embed(ctx, model, 3, []string{"alpha"})
	if err == nil {
		t.Fatal("expected the cancelled call to fail")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected a recognisable cancellation, got: %v", err)
	}
}

// Nothing that reaches a log, a panic or a tool result may carry the key.
func TestFormattingAnEndpointRedactsItsKey(t *testing.T) {
	e, err := embedding.At("http://127.0.0.1:11434", "sk-local-secret")
	if err != nil {
		t.Fatalf("At: %v", err)
	}

	for _, rendered := range []string{
		fmt.Sprintf("%v", e), fmt.Sprintf("%+v", e), fmt.Sprintf("%#v", e), e.String(),
	} {
		if strings.Contains(rendered, "sk-local-secret") {
			t.Errorf("the key appeared in %q", rendered)
		}
	}
}

// The three spellings people actually write must reach the same place. Guessing
// wrong produces /v1/embeddings/v1/embeddings, a 404 that reads like the service
// is down rather than like a typo.
func TestEveryUrlSpellingResolvesToTheSameEndpoint(t *testing.T) {
	for _, suffix := range []string{"", "/", "/v1", "/v1/", "/v1/embeddings"} {
		t.Run("base"+suffix, func(t *testing.T) {
			got := &captured{}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got.path = r.URL.Path
				answering([]float32{0, 0, 0})(w, r)
			}))
			defer srv.Close()

			e, err := embedding.At(srv.URL+suffix, "")
			if err != nil {
				t.Fatalf("At: %v", err)
			}
			if _, err := e.Embed(context.Background(), model, 3, []string{"alpha"}); err != nil {
				t.Fatalf("embed: %v", err)
			}
			if got.path != "/v1/embeddings" {
				t.Errorf("expected /v1/embeddings, got %s", got.path)
			}
		})
	}
}

// Not configuring an endpoint is the normal case, and it must be distinguishable
// from configuring a broken one.
func TestNoUrlMeansNoEndpointAndNoError(t *testing.T) {
	e, err := embedding.At("  ", "")
	if err != nil {
		t.Fatalf("expected an unset URL to be accepted, got %v", err)
	}
	if e != nil {
		t.Errorf("expected no endpoint, got %v", e)
	}
}

// Each of these would send the text this feature exists to protect somewhere
// the operator did not intend, so each stops the server rather than starting
// one that quietly does the wrong thing.
func TestAUrlThatWouldLeakOrConfuseIsRefused(t *testing.T) {
	for _, c := range []struct {
		name string
		url  string
		says string
	}{
		{"plaintext to another host", "http://example.com/v1/embeddings", "clear"},
		{"plaintext to a private address", "http://10.0.0.5/v1/embeddings", "clear"},
		{"a scheme that is not http", "ftp://127.0.0.1/v1/embeddings", "http"},
		{"credentials in the URL", "http://user:pass@127.0.0.1/v1/embeddings", "credentials"},
		{"a query string", "http://127.0.0.1/v1/embeddings?key=abc", "query string"},
		{"a fragment", "http://127.0.0.1/v1/embeddings#section", "query string"},
		{"an unexpanded placeholder", "${NOETIVE_EMBEDDINGS_URL}", "substitute"},
		{"no host", "http:///v1/embeddings", "host"},
	} {
		t.Run(c.name, func(t *testing.T) {
			e, err := embedding.At(c.url, "")
			if err == nil {
				t.Fatalf("expected %s to be refused, got %v", c.url, e)
			}
			if !strings.Contains(err.Error(), c.says) {
				t.Errorf("expected the refusal to explain %q, got: %v", c.says, err)
			}
		})
	}
}

// The largest dimensionality the wire accepts must be accepted here too: a
// boundary that refuses the last legal size is as wrong as one that admits an
// illegal one.
func TestTheLargestAllowedDimensionalityReachesTheEndpoint(t *testing.T) {
	e, got := serving(t, func(w http.ResponseWriter, r *http.Request) {
		answering(make([]float32, 4096))(w, r)
	})

	if _, err := e.Embed(context.Background(), model, 4096, []string{"alpha"}); err != nil {
		t.Fatalf("expected 4096 dimensions to be accepted, got %v", err)
	}
	if !got.seen {
		t.Error("the endpoint was never called")
	}
}

// https is allowed anywhere, because an operator running a private gateway is a
// real deployment and TLS is the control there.
func TestHttpsIsAllowedToAnyHost(t *testing.T) {
	if _, err := embedding.At("https://embeddings.internal.example/v1", ""); err != nil {
		t.Errorf("expected an https endpoint to be accepted, got %v", err)
	}
}

// Loopback in every spelling, because these are what people actually run.
func TestLoopbackIsAllowedOverPlaintext(t *testing.T) {
	for _, url := range []string{
		"http://127.0.0.1:11434", "http://localhost:11434", "http://[::1]:11434", "http://127.7.7.7:8080",
	} {
		if _, err := embedding.At(url, ""); err != nil {
			t.Errorf("expected %s to be accepted, got %v", url, err)
		}
	}
}

// An unexpanded key would be sent as a bearer and come back unauthorized,
// pointing the operator at a credential that is fine.
func TestAnUnexpandedKeyIsRefused(t *testing.T) {
	if _, err := embedding.At("http://127.0.0.1:11434", "${NOETIVE_EMBEDDINGS_KEY_SECRET}"); err == nil {
		t.Fatal("expected an unexpanded key to be refused")
	}
}

// Each of these would be rejected by the endpoint, or worse accepted. Failing
// before the request is sent means a mistake costs nothing.
func TestBadInputNeverReachesTheEndpoint(t *testing.T) {
	for _, c := range []struct {
		name       string
		model      string
		dimensions uint16
		texts      []string
	}{
		{"no model", "", 3, []string{"alpha"}},
		{"no dimensionality", model, 0, []string{"alpha"}},
		{"more dimensions than the wire allows", model, 4097, []string{"alpha"}},
		{"nothing to embed", model, 3, nil},
		{"a blank text", model, 3, []string{"alpha", "   \t\n "}},
		{"an empty text", model, 3, []string{""}},
	} {
		t.Run(c.name, func(t *testing.T) {
			e, got := serving(t, answering([]float32{0, 0, 0}))

			if _, err := e.Embed(context.Background(), c.model, c.dimensions, c.texts); err == nil {
				t.Fatal("expected the call to be refused")
			}
			if got.seen {
				t.Error("the endpoint was called anyway")
			}
		})
	}
}
