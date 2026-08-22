// Package targeting owns the namespace, model and dimensions triple that
// routes every Semantik publish, search and subscribe.
//
// The triple is required on every request and is never defaulted. Substituting
// a value for an unset field, most dangerously turning an empty namespace into
// a shared one such as "global", would route a caller's data into a namespace
// they never named, turning a forgotten field into a cross-tenant leak instead
// of an error. Model and dimensions are model-coupled properties with no safe
// default: a wrong guess silently changes what gets embedded and matched.
//
// An operator may configure a fallback triple when starting the server. That is
// naming, not defaulting: the values came from a human who chose them. A field
// that neither the tool call nor the configuration names is an error that says
// which field is missing.
//
// An operator may also close the shared namespace entirely. Naming a namespace
// correctly is no help to a tenant whose agents keep reaching for the one that
// spans tenants, and "global" is the value every piece of guidance uses as its
// example. Policy carries that decision alongside the fallback, because both
// are standing statements by the same operator and both apply to the same call.
package targeting

import (
	"fmt"
	"strconv"
	"strings"
)

// Environment variables read by FromEnv.
const (
	EnvNamespace     = "NOETIVE_NAMESPACE"
	EnvModel         = "NOETIVE_MODEL"
	EnvDimensions    = "NOETIVE_DIMENSIONS"
	EnvDisableGlobal = "NOETIVE_DISABLE_GLOBAL_NS"
)

// GlobalNamespace is the shared namespace that spans tenants. It is the one
// namespace whose name means the same thing to everybody, which is what makes
// it both useful and the one worth being able to close.
const GlobalNamespace = "global"

// EnvNames lists every environment variable this package reads, in the order
// they are documented.
//
// Exported so the packaging emitter can check server.json against it rather
// than keeping a second list that drifts. Five variables spread over two
// environmentVariables blocks and a set of OCI runtime arguments is exactly the
// kind of repetition that goes stale without a check.
func EnvNames() []string {
	return []string{EnvNamespace, EnvModel, EnvDimensions, EnvDisableGlobal}
}

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

// ForbiddenError names a namespace the operator has closed on this server.
//
// It is distinct from MissingError because the remedy is different: nothing was
// forgotten, the call named a destination it may not use, and the agent has to
// pick another one rather than supply a field.
type ForbiddenError struct {
	Namespace string
}

func (e *ForbiddenError) Error() string {
	return fmt.Sprintf("targeting: the %q namespace is closed on this server (%s); name the namespace your work belongs in", e.Namespace, EnvDisableGlobal)
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
// legitimately incomplete: the missing fields are expected to arrive on the
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

// Policy is what an operator decided when they started the server: which fields
// a tool call may leave unset, and whether the shared namespace is available.
//
// The two travel together because they are answered by the same person at the
// same moment and consulted at the same point in every call. Passing a bare
// fallback around and checking the namespace somewhere else is how one of the
// three tools ends up without the check.
//
// The zero Policy configures nothing and closes nothing, which is how a bare
// `npx @noetive/mcp-server` starts.
//
// Field ordering: Target (34 B) > bool (1 B).
type Policy struct {
	Fallback       Target
	GlobalDisabled bool
}

// Resolve layers a per-call triple over the configured fallback, requires the
// result to be complete, and refuses a namespace the operator closed.
//
// This is the call-time check: by the time a request is about to be sent, every
// field must be named and the destination must be one this server may use.
//
//	policy := targeting.Policy{Fallback: targeting.Target{Model: "Qwen3-Embedding-4B", Dimensions: 1024}}
//	target, err := policy.Resolve(targeting.Target{Namespace: "incidents"})
//	// target == {Namespace: "incidents", Model: "Qwen3-Embedding-4B", Dimensions: 1024}
func (p Policy) Resolve(call Target) (Target, error) {
	resolved := Layer(call, p.Fallback)
	if err := resolved.Validate(); err != nil {
		return Target{}, err
	}
	if err := p.Allows(resolved.Namespace); err != nil {
		return Target{}, err
	}
	return resolved, nil
}

// Allows reports whether this server may route to a namespace.
//
// The comparison ignores case because namespace names are case-insensitive:
// "Global" and "global" are one namespace, not two. Matching exactly would
// leave the closed namespace reachable by capitalising it, which is a spelling
// a model produces without being asked. Surrounding space is trimmed for the
// same reason.
//
// The routing namespace is the only place this needs checking. A SemQL query
// may carry a NAMESPACE clause of its own, but the broker scopes both search
// and subscribe on the request's namespace field and never on the query's
// selector, so scanning query text would refuse anchors like "global warming"
// while protecting nothing.
func (p Policy) Allows(namespace string) error {
	if !p.GlobalDisabled {
		return nil
	}
	if strings.EqualFold(strings.TrimSpace(namespace), GlobalNamespace) {
		return &ForbiddenError{Namespace: namespace}
	}
	return nil
}

// FromEnv reads the operator's policy from the environment through lookup,
// which is os.Getenv in production and a map read in tests.
//
// A partially-populated fallback is normal and is not an error: the missing
// fields are expected to arrive on the tool call. An unparseable value in
// either of the two typed variables fails, because a typo there would otherwise
// be silently discarded: a dropped dimensionality reappears as an opaque
// model_not_provisioned error from the server, and a dropped
// NOETIVE_DISABLE_GLOBAL_NS leaves the shared namespace open on a server whose
// operator believes they closed it.
//
//	policy, err := targeting.FromEnv(os.Getenv)
func FromEnv(lookup func(string) string) (Policy, error) {
	p := Policy{
		Fallback: Target{
			Namespace: lookup(EnvNamespace),
			Model:     lookup(EnvModel),
		},
	}

	if raw := lookup(EnvDimensions); raw != "" {
		dims, err := strconv.ParseUint(raw, 10, 16)
		if err != nil {
			return Policy{}, fmt.Errorf("targeting: %s=%q is not a number between 1 and 65535: %w", EnvDimensions, raw, err)
		}
		if dims == 0 {
			return Policy{}, fmt.Errorf("targeting: %s=0 is not a usable dimensionality", EnvDimensions)
		}
		p.Fallback.Dimensions = uint16(dims)
	}

	disabled, err := ParseDisableGlobal(lookup(EnvDisableGlobal))
	if err != nil {
		return Policy{}, err
	}
	p.GlobalDisabled = disabled

	return p, nil
}

// ParseDisableGlobal reads the spelling of a boolean an operator actually
// types. Empty means unset, which leaves the shared namespace open.
//
// Anything else is refused rather than read as false. strconv.ParseBool alone
// would reject "yes" and "on", which a person writing a config file reasonably
// expects to work, and treating an unrecognised value as false would turn
// NOETIVE_DISABLE_GLOBAL_NS=ture into a server that silently permits exactly
// what it was told to forbid.
func ParseDisableGlobal(raw string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "":
		return false, nil
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	default:
		return false, fmt.Errorf("targeting: %s=%q is not a yes or no value; use 1, true, yes, on, 0, false, no or off", EnvDisableGlobal, raw)
	}
}
