package mcpserver_test

import (
	"testing"

	"github.com/noetive/noetive-mcp/internal/mcpserver"
)

// An editor that does not substitute its config placeholder hands the server
// the literal text. It is a well-formed, non-empty string, so nothing
// downstream rejects it: the request goes out and comes back "unauthorized",
// sending the user to check their account when the fault is an environment
// variable their editor never saw. Catching it by shape is the only way to name
// the real cause.
func TestUnexpandedPlaceholdersAreRecognised(t *testing.T) {
	placeholders := []string{
		"${NOETIVE_KEY_SECRET}",
		"$NOETIVE_KEY_SECRET",
		"%NOETIVE_KEY_SECRET%",
		"  ${NOETIVE_KEY_SECRET}  ",
	}

	for _, value := range placeholders {
		t.Run(value, func(t *testing.T) {
			if !mcpserver.PlaceholderKey(value) {
				t.Errorf("expected %q to be recognised as an unexpanded placeholder", value)
			}
		})
	}
}

// A real key must never be mistaken for a placeholder — refusing a working
// credential would be a far worse failure than the one this guards against.
func TestRealKeysAreNotMistakenForPlaceholders(t *testing.T) {
	keys := []string{
		"keyu_3xAmPl3Base58Value",
		"keyt_3xAmPl3Base58Value",
		"",
		"   ",
		"not a key but not a placeholder",
	}

	for _, value := range keys {
		t.Run(value, func(t *testing.T) {
			if mcpserver.PlaceholderKey(value) {
				t.Errorf("expected %q not to be treated as a placeholder", value)
			}
		})
	}
}

// The check has to require both halves of the placeholder shape. A value that
// merely ends in a brace — which a base58 key never does, but a passphrase-style
// credential might — must not be mistaken for an unexpanded variable and
// refused, because refusing a working credential is worse than the failure this
// guards against.
func TestBothHalvesOfThePlaceholderShapeAreRequired(t *testing.T) {
	notPlaceholders := []string{
		"keyu_ends_with_a_brace}",
		"}",
		"keyu_has_a_${inside}_but_starts_normally",
	}

	for _, value := range notPlaceholders {
		t.Run(value, func(t *testing.T) {
			if mcpserver.PlaceholderKey(value) {
				t.Errorf("expected %q to be treated as a real key", value)
			}
		})
	}
}
