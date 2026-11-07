package mcpserver_test

import (
	"testing"

	"go.noetive.io/noetive-mcp/internal/mcpserver"
)

// A user key has to be named for what it is, or the server starts "healthy"
// and every call is refused by the service with nothing pointing at the key.
// Padding is included because editors and shells leave it in.
func TestUserKeyRecognisesUserKeys(t *testing.T) {
	for _, value := range []string{"keyu_3xAmPl3Base58Value", " keyu_x ", "keyu_"} {
		if !mcpserver.UserKey(value) {
			t.Errorf("UserKey(%q) = false, want true", value)
		}
	}
}

// The check is a blocklist: agent keys, access tokens and shapes this
// repository has never seen must all still reach the service, which is the
// authority on what it accepts.
func TestUserKeyLetsEveryOtherShapeThrough(t *testing.T) {
	for _, value := range []string{
		"keya_3xAmPl3Base58Value",
		"eyJhbGciOiJFUzI1NiJ9.eyJzdWIiOiJhZ18xIn0.c2ln",
		"keyz_unknown_family",
		"keyu",
		"xkeyu_abc",
		"KEYU_abc",
		"",
	} {
		if mcpserver.UserKey(value) {
			t.Errorf("UserKey(%q) = true, want false", value)
		}
	}
}
