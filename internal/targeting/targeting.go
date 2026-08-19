// Package targeting owns the namespace, model and dimensions triple that
// routes every Semantik publish, search and subscribe.
//
// The triple is required on every request and is never defaulted. Substituting
// a value for an unset field — most dangerously turning an empty namespace into
// a shared one such as "global" — would route a caller's data into a namespace
// they never named, turning a forgotten field into a cross-tenant leak instead
// of an error. Model and dimensions are model-coupled properties with no safe
// default: a wrong guess silently changes what gets embedded and matched.
//
// An operator may configure a fallback triple when starting the server. That is
// naming, not defaulting: the values came from a human who chose them. A field
// that neither the tool call nor the configuration names is an error that says
// which field is missing.
package targeting

import (
	"fmt"
	"strconv"
)

// Environment variables read by FromEnv.
const (
	EnvNamespace  = "NOETIVE_NAMESPACE"
	EnvModel      = "NOETIVE_MODEL"
	EnvDimensions = "NOETIVE_DIMENSIONS"
)

// Target is a fully-specified routing triple.
//
// The zero value is not usable; Validate reports which field is missing.
//
// Field ordering: strings (16 B each) > uint16 (2 B).
type Target struct {
	Namespace  string
	Model      string
	Dimensions uint16
}

// MissingError names the field that neither the caller nor the configuration
// supplied. It is returned instead of a silent substitution so the failure is
// visible at the call site rather than as data in the wrong namespace.
type MissingError struct {
	Field string
}

func (e *MissingError) Error() string {
	return fmt.Sprintf("targeting: %s is required and has no configured value; pass it in the tool call or start the server with it configured", e.Field)
}

// Validate reports the first unset field of t.
//
//	if err := target.Validate(); err != nil { return err }
func (t Target) Validate() error {
	switch {
	case t.Namespace == "":
		return &MissingError{Field: "namespace"}
	case t.Model == "":
		return &MissingError{Field: "model"}
	case t.Dimensions == 0:
		return &MissingError{Field: "dimensions"}
	}
	return nil
}

// Layer merges two triples field by field without judging the result. A field
// set in over wins; an unset field takes under's value; a field unset in both
// stays unset.
//
// Merging is separate from validating because the two happen at different
// times. At startup, flags are layered over the environment and the result is
// legitimately incomplete — the missing fields are expected to arrive on the
// tool call. Validating there would refuse to start a server that has nothing
// configured, which is exactly how an editor launches it by default.
//
//	configured := targeting.Layer(fromFlags, fromEnvironment)
func Layer(over, under Target) Target {
	merged := over
	if merged.Namespace == "" {
		merged.Namespace = under.Namespace
	}
	if merged.Model == "" {
		merged.Model = under.Model
	}
	if merged.Dimensions == 0 {
		merged.Dimensions = under.Dimensions
	}
	return merged
}

// Resolve layers a per-call triple over a configured fallback and requires the
// result to be complete. This is the call-time check: by the time a request is
// about to be sent, every field must be named.
//
//	target, err := targeting.Resolve(
//	    targeting.Target{Namespace: "incidents"},
//	    targeting.Target{Model: "Qwen3-Embedding-4B", Dimensions: 1024},
//	)
//	// target == {Namespace: "incidents", Model: "Qwen3-Embedding-4B", Dimensions: 1024}
func Resolve(call, fallback Target) (Target, error) {
	resolved := Layer(call, fallback)
	if err := resolved.Validate(); err != nil {
		return Target{}, err
	}
	return resolved, nil
}

// FromEnv reads a fallback triple from the environment through lookup, which is
// os.Getenv in production and a map read in tests.
//
// A partially-populated result is normal and is not an error: the missing
// fields are expected to arrive on the tool call. Only an unparseable
// dimensions value fails, because a typo there would otherwise be silently
// discarded and reappear as a model_not_provisioned error from the server.
//
//	fallback, err := targeting.FromEnv(os.Getenv)
func FromEnv(lookup func(string) string) (Target, error) {
	t := Target{
		Namespace: lookup(EnvNamespace),
		Model:     lookup(EnvModel),
	}

	raw := lookup(EnvDimensions)
	if raw == "" {
		return t, nil
	}

	dims, err := strconv.ParseUint(raw, 10, 16)
	if err != nil {
		return Target{}, fmt.Errorf("targeting: %s=%q is not a number between 1 and 65535: %w", EnvDimensions, raw, err)
	}
	if dims == 0 {
		return Target{}, fmt.Errorf("targeting: %s=0 is not a usable dimensionality", EnvDimensions)
	}

	t.Dimensions = uint16(dims)
	return t, nil
}
