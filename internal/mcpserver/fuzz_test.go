package mcpserver_test

import (
	"strings"
	"testing"

	"go.noetive.io/noetive-mcp/internal/mcpserver"
)

// FuzzPlaceholderKeyNeverRefusesARealKey is the asymmetry this check has to keep.
//
// [mcpserver.PlaceholderKey] exists for one failure: an editor writes
// `${NOETIVE_KEY_SECRET}` into a config, nothing expands it, and the literal text
// arrives as the key. Saying so at startup is far better than a 401 the user
// reads as a bad key.
//
// The two mistakes it can make are not equal. Missing a placeholder costs one
// confusing refusal. Calling a real key a placeholder refuses a credential that
// works, on a machine where nothing is wrong, so the target asserts in that
// direction: anything that is not literally a variable reference must pass.
//
// Nothing here parses the key itself (only [mcpserver.UserKey] looks at a
// prefix, and only at keyu_). A prefix this repository has never seen is
// a key like any other; the server is the only authority on validity.
func FuzzPlaceholderKeyNeverRefusesARealKey(f *testing.F) {
	f.Add("keya_3xAmPl3Base58Value")
	f.Add("keyz_a_family_that_does_not_exist_yet")
	f.Add("no_prefix_at_all")
	f.Add("a key with spaces")
	f.Add("ends_with_a_brace}")
	f.Add("has_a_${inside}_but_starts_normally")
	f.Add("100% not a placeholder")
	f.Add("\xff\xfe invalid utf-8")
	f.Add(strings.Repeat("A", 64<<10))
	f.Add("")
	f.Add("   ")

	f.Fuzz(func(t *testing.T, key string) {
		got := mcpserver.PlaceholderKey(key)
		if !got {
			return
		}

		// It said placeholder. That is only defensible for the three shapes an
		// unexpanded variable actually takes; anything else is a working
		// credential being refused on a machine where nothing is wrong.
		trimmed := strings.TrimSpace(key)
		switch {
		case strings.HasPrefix(trimmed, "${") && strings.HasSuffix(trimmed, "}"):
		case strings.HasPrefix(trimmed, "$") && !strings.Contains(trimmed, " "):
		case strings.HasPrefix(trimmed, "%") && strings.HasSuffix(trimmed, "%") && len(trimmed) > 1:
		default:
			t.Fatalf("a key that is not a variable reference was refused as a placeholder: %q", key)
		}
	})
}

// FuzzUserKeyRefusesOnlyUserKeys keeps the user-key check a blocklist.
//
// [mcpserver.UserKey] refuses keyu_ because every service the tools reach does,
// and saying so at startup beats a "healthy" server whose every call fails. The
// cost of over-reaching is a working credential refused on a machine where
// nothing is wrong, so anything not starting keyu_ must pass.
func FuzzUserKeyRefusesOnlyUserKeys(f *testing.F) {
	f.Add("keya_3xAmPl3Base58Value")
	f.Add("keyu_3xAmPl3Base58Value")
	f.Add("  keyu_padded  ")
	f.Add("eyJhbGciOiJFUzI1NiJ9.eyJzdWIiOiJhZ18xIn0.c2ln")
	f.Add("keyu")
	f.Add("xkeyu_")
	f.Add("")

	f.Fuzz(func(t *testing.T, key string) {
		want := strings.HasPrefix(strings.TrimSpace(key), "keyu_")
		if got := mcpserver.UserKey(key); got != want {
			t.Fatalf("UserKey(%q) = %v, want %v", key, got, want)
		}
	})
}
