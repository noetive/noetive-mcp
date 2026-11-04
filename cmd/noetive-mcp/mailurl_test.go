package main

import "testing"

// TestTheKeyIsSentOnlyWhereItIsSafe covers NOETIVE_BUD_BASE_URL: the Noetive key
// travels with every request, so an address that would send it in the clear,
// or one that is not an address at all, must stop the server rather than be
// used. Loopback http is allowed because there is no network to cross.
func TestTheKeyIsSentOnlyWhereItIsSafe(t *testing.T) {
	for raw, ok := range map[string]bool{
		"":                           true,
		"https://bud.noetive.io":     true,
		"  https://bud.example.com ": true,
		"http://localhost:8080":      true,
		"http://127.0.0.1:8080":      true,
		"http://[::1]:8080":          true,
		"http://bud.example.com":     false,
		"http://10.0.0.5":            false,
		"ftp://bud.example.com":      false,
		"bud.noetive.io":             false,
		"https://":                   false,
		"https://user:pw@bud.io":     false,
		"://":                        false,
	} {
		if err := checkMailURL(raw); (err == nil) != ok {
			t.Errorf("%q: accepted %v, want %v (%v)", raw, err == nil, ok, err)
		}
	}
}

// TestABlankKeyLeavesTheServerDegraded: a whitespace-only key is no key, so
// the mail tools are left out and the server starts in its degraded state
// instead of exiting and taking the Semantik tools down with it.
func TestABlankKeyLeavesTheServerDegraded(t *testing.T) {
	t.Setenv("NOETIVE_KEY_SECRET", "   ")
	client, err := resolveMailer()
	if err != nil || client != nil {
		t.Errorf("a blank key gave client %v, error %v", client, err)
	}
}

// TestAnUnsafeBudURLStopsTheServer covers the wiring, not only the check: with
// a key set, an http address to another host must stop startup, and a sound
// one must give a client. Without this the check could be skipped and every
// test above would still pass.
func TestAnUnsafeBudURLStopsTheServer(t *testing.T) {
	t.Setenv("NOETIVE_KEY_SECRET", "keya_test")

	t.Setenv("NOETIVE_BUD_BASE_URL", "http://bud.example.com")
	if client, err := resolveMailer(); err == nil || client != nil {
		t.Errorf("an http address to another host gave client %v, error %v", client, err)
	}

	t.Setenv("NOETIVE_BUD_BASE_URL", "https://bud.example.com")
	if client, err := resolveMailer(); err != nil || client == nil {
		t.Errorf("an https address gave client %v, error %v", client, err)
	}
}
