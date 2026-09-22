package targeting_test

import (
	"strings"
	"testing"

	"github.com/noetive/noetive-mcp/internal/targeting"
)

// The routing triple is chosen by a model, and it is the only thing standing
// between one tenant's data and another's. A tool call names the namespace it
// wants; nothing about the caller constrains that name. So the adversary these
// targets assume is a model that has read something it should not have (a
// poisoned search result, a crafted message) and is now naming destinations on
// somebody else's behalf.
//
// Two mistakes matter, and they are opposites. Routing somewhere nobody named is
// a cross-tenant write with no switch to turn it off. Refusing a namespace that
// was legitimately named breaks a working install. Each target below fixes one
// of those directions.

// FuzzResolveNeverInventsADestination is the anti-default property.
//
// Every field of a resolved triple has to have come from the call or from what
// the operator configured. Not a constant, not a fallback of a fallback, not a
// normalised form of something nearby. The failure this prevents is the one
// docs/security.md calls out: an empty namespace quietly becoming a shared one,
// turning a field the model forgot into a cross-tenant write that nobody sees.
//
// Stated as provenance rather than as "no empty fields" on purpose. A check for
// emptiness passes happily for a default somebody adds later; a check that every
// value is traceable to an input does not.
func FuzzResolveNeverInventsADestination(f *testing.F) {
	f.Add("incidents", "model-a", 1024, "", "", 0, false)
	f.Add("", "", 0, "fallback-ns", "fallback-model", 512, false)
	f.Add("", "", 0, "", "", 0, false)
	f.Add("  ", "\x00", -1, "", "", 0, true)
	f.Add("global", "m", 8, "", "", 0, true)
	f.Add("\xff\xfe", "\ufeff", 65536, "under", "u", 1, false)

	f.Fuzz(func(t *testing.T, ns, model string, dims int, fbNS, fbModel string, fbDims int, disable bool) {
		if dims < 0 {
			dims = -dims
		}
		if fbDims < 0 {
			fbDims = -fbDims
		}

		call := targeting.Target{Namespace: ns, Model: model, Dimensions: uint16(dims % 65536)}
		policy := targeting.Policy{
			Fallback:       targeting.Target{Namespace: fbNS, Model: fbModel, Dimensions: uint16(fbDims % 65536)},
			GlobalDisabled: disable,
		}

		got, err := policy.Resolve(call)
		if err != nil {
			// A refusal routes nothing, which is always a safe answer.
			return
		}

		if got.Namespace != call.Namespace && got.Namespace != policy.Fallback.Namespace {
			t.Fatalf("resolved to a namespace neither the call nor the operator named: %q", got.Namespace)
		}
		if got.Model != call.Model && got.Model != policy.Fallback.Model {
			t.Fatalf("resolved to a model neither the call nor the operator named: %q", got.Model)
		}
		if got.Dimensions != call.Dimensions && got.Dimensions != policy.Fallback.Dimensions {
			t.Fatalf("resolved to a dimensionality neither side named: %d", got.Dimensions)
		}

		// And the triple is whole. An unset field reaching the wire is a request
		// the server answers about a destination the caller never chose.
		if got.Namespace == "" || got.Model == "" || got.Dimensions == 0 {
			t.Fatalf("resolve succeeded with an unset field: %+v", got)
		}
	})
}

// FuzzAClosedNamespaceStaysClosed is the direction that costs data.
//
// When the operator closed the shared namespace, no spelling of it may resolve.
// Case is the spelling a model produces without being asked, it will write
// "Global" because the sentence began there, and surrounding space is what
// survives a copy out of a log line.
//
// What this target can prove stops at the edge of this process: it asserts that
// nothing we accept is a case-folded, space-trimmed match for the shared name.
// Whether the broker folds a name this comparison does not, a fullwidth or
// otherwise decomposed spelling, is the broker's question, and the reason this
// switch is a convenience rather than the boundary. The boundary is the server,
// which knows the caller and its grants.
func FuzzAClosedNamespaceStaysClosed(f *testing.F) {
	f.Add("global")
	f.Add("GLOBAL")
	f.Add("GlObAl")
	f.Add("  global  ")
	f.Add("\tglobal\n")
	f.Add("global\x00")
	f.Add("ｇｌｏｂａｌ")
	f.Add("gl\u200bobal")
	f.Add("globally")
	f.Add("global-incidents")

	f.Fuzz(func(t *testing.T, namespace string) {
		policy := targeting.Policy{GlobalDisabled: true}

		if err := policy.Allows(namespace); err != nil {
			return
		}
		if strings.EqualFold(strings.TrimSpace(namespace), "global") {
			t.Fatalf("the closed namespace was reachable as %q", namespace)
		}
	})
}

// FuzzAnOpenNamespaceIsNeverRefused is the mirror, and it guards a subtler bug.
//
// The obvious way to "harden" the check above is to widen it: a prefix test, a
// substring test, a normaliser that strips punctuation. Each of those silently
// refuses real namespaces: "global-incidents" and "globally" are somebody's
// data, and a client that will not route to them is broken in a way that looks
// like a server problem from the outside.
//
// So a refusal has to be exact. Nothing else may be caught by it.
func FuzzAnOpenNamespaceIsNeverRefused(f *testing.F) {
	f.Add("globally", true)
	f.Add("global-incidents", true)
	f.Add("myglobal", true)
	f.Add("glob", true)
	f.Add("global", false)
	f.Add("incidents", true)
	f.Add("", true)

	f.Fuzz(func(t *testing.T, namespace string, disable bool) {
		policy := targeting.Policy{GlobalDisabled: disable}

		err := policy.Allows(namespace)
		if err == nil {
			return
		}
		if !disable {
			t.Fatalf("a namespace was refused on a server that closed nothing: %q", namespace)
		}
		if !strings.EqualFold(strings.TrimSpace(namespace), "global") {
			t.Fatalf("a namespace that is not the shared one was refused: %q", namespace)
		}
	})
}
