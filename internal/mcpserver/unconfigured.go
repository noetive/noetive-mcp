package mcpserver

import (
	"context"
	"strings"

	"github.com/noetive/noetive-sdk-go/semantik"
)

// PlaceholderKey reports whether value is an unexpanded variable reference
// rather than an API key.
//
// Editors are configured with "${NOETIVE_KEY_SECRET}" so the secret stays out
// of the config file, and the editor substitutes the real value at launch. When
// that substitution does not happen — the variable is unset, or the editor was
// started from a desktop launcher that never read the user's shell profile —
// the literal placeholder arrives here instead.
//
// It has to be caught by shape, because it is a perfectly well-formed string:
// it is not empty, so nothing downstream rejects it, and the request reaches
// the server and comes back "unauthorized". That answer sends the user to check
// their account, when the actual fault is an environment variable their editor
// could not see.
//
//	if mcpserver.PlaceholderKey(os.Getenv("NOETIVE_KEY_SECRET")) { ... }
func PlaceholderKey(value string) bool {
	trimmed := strings.TrimSpace(value)

	// ${VAR} and $VAR as written in an editor config, and %VAR% as Windows
	// writes it.
	switch {
	case strings.HasPrefix(trimmed, "${") && strings.HasSuffix(trimmed, "}"):
		return true
	case strings.HasPrefix(trimmed, "$") && !strings.Contains(trimmed, " "):
		return true
	case strings.HasPrefix(trimmed, "%") && strings.HasSuffix(trimmed, "%") && len(trimmed) > 1:
		return true
	}
	return false
}

// Unconfigured is a Broker that refuses every call with the same explanation.
//
// It exists so a server with no usable credential still starts. Exiting instead
// would leave the editor reporting only that the server failed to launch, with
// no tools registered and nothing to ask — including noetive_health, which is
// the tool whose entire job is to say what is wrong. Starting degraded turns an
// opaque launch failure into a sentence the agent can read out and the user can
// act on.
//
// The refusal is shaped as a pre-flight *semantik.Error (HTTPStatus zero) so it
// travels the same path as any other rejection that never reached the wire.
//
//	srv := mcpserver.New(version, mcpserver.Unconfigured(err.Error()), fallback)
func Unconfigured(reason string) Broker {
	return &unconfigured{err: &semantik.Error{
		Code:    semantik.CodeUnauthorized,
		Message: reason,
	}}
}

type unconfigured struct {
	err *semantik.Error
}

func (u *unconfigured) Publish(context.Context, semantik.PublishRequest) (semantik.PublishResponse, error) {
	return semantik.PublishResponse{}, u.err
}

func (u *unconfigured) Search(context.Context, semantik.SearchRequest) (semantik.SearchResponse, error) {
	return semantik.SearchResponse{}, u.err
}

func (u *unconfigured) Subscribe(context.Context, semantik.SubscribeRequest) (*semantik.Subscription, error) {
	return nil, u.err
}

func (u *unconfigured) Lint(context.Context, semantik.LintRequest) (semantik.LintResponse, error) {
	return semantik.LintResponse{}, u.err
}

func (u *unconfigured) Health(context.Context) error { return u.err }
