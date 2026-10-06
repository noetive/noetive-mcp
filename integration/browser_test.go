//go:build integration

package integration

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"go.noetive.io/noetive-mcp/internal/browser"
	"go.noetive.io/noetive-mcp/internal/mcpserver"
	"go.noetive.io/noetive-mcp/internal/targeting"
)

// The browser tests drive a real riffle, found the way the server finds it.
// They catch what the fake riffle in the unit tests cannot: riffle renaming a
// tool, changing the flags of its mcp verb, or answering in a shape the relay
// no longer passes through.

// pageText is what the generated page says, so a reply that carries it proves
// the page was loaded and read through the relay.
const pageText = "A generated page for the relay check"

// riffleDaemon starts a riffle daemon on a socket of its own, so the test
// neither uses nor disturbs the daemon on the machine's default socket, and
// returns the riffle executable and that socket.
//
// The daemon is allowed private addresses because the page is served on
// loopback. It is stopped with SIGTERM, which closes its browsers; a kill would
// leave Chrome running.
func riffleDaemon(t *testing.T) (path, socket string) {
	t.Helper()

	path, _ = browser.Locate(os.Getenv, exec.LookPath)
	if path == "" {
		t.Skipf("%s on PATH or in %s; skipping the browser tests", riffleNotFound, browser.EnvBin)
	}

	// Not t.TempDir: a socket path is limited to about a hundred bytes, which
	// the test name and the system temporary directory can exceed on their own.
	dir, err := os.MkdirTemp("", "rf")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket = filepath.Join(dir, "s.sock")

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, path, "serve", "-socket", socket, "-allow-private", "-daemon-idle", "30s")
	// The daemon gets what the server gives riffle: everything but the Noetive
	// configuration, which the suite may hold and a browser has no use for.
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "NOETIVE_") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Stderr = os.Stderr
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 10 * time.Second
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatalf("could not start the riffle daemon: %v", err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	t.Cleanup(func() {
		cancel()
		<-exited
	})

	// Until the daemon listens, a riffle client starts a daemon of its own,
	// without this one's policy and outliving the test.
	deadline := time.After(10 * time.Second)
	for {
		conn, err := (&net.Dialer{}).DialContext(ctx, "unix", socket)
		if err == nil {
			_ = conn.Close()
			return path, socket
		}
		select {
		case err := <-exited:
			exited <- err
			t.Fatalf("the riffle daemon exited before it listened: %v", err)
		case <-deadline:
			t.Fatalf("the riffle daemon did not listen on %s: %v", socket, err)
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// browserServer assembles the server an editor would get with riffle installed
// and no Noetive key, relaying to a riffle MCP server on socket.
func browserServer(t *testing.T, path, socket string) *session {
	t.Helper()

	relay, err := browser.Start(context.Background(), path, nil, "-socket", socket)
	if err != nil {
		t.Fatalf("could not start riffle: %v", err)
	}
	t.Cleanup(func() { _ = relay.Close() })

	broker := mcpserver.Unconfigured("the browser tests need no key")
	return open(t, mcpserver.New("integration", broker, targeting.Policy{}, mcpserver.WithBrowser(relay)))
}

func generatedPage(t *testing.T) string {
	t.Helper()

	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprintf(w, "<!doctype html><title>Relay check</title><main><h1>%s</h1></main>", pageText)
	}))
	t.Cleanup(page.Close)
	return page.URL
}

// seatsHint is added to a failure that may have been the machine's browser
// limit rather than the relay: the test daemon shares that limit with every
// other riffle this user runs.
const seatsHint = "if riffle says no browser is free, close other riffle sessions on this machine (riffle allows four at once) and rerun"

// The relay end to end: tools listed by riffle, a call passed through, the
// answer passed back, and a failed step still reported as one.
func TestTheBrowserToolsReachARealRiffle(t *testing.T) {
	path, socket := riffleDaemon(t)
	s := browserServer(t, path, socket)
	url := generatedPage(t)

	help := s.call("browser_view", map[string]any{"view": "help"})
	if help.IsError || !strings.Contains(s.text(help), "goto") {
		t.Fatalf("expected the program guide, got: %s", s.text(help))
	}

	read := s.call("browser_run", map[string]any{"program": "goto " + url + "\nview read"})
	if read.IsError || !strings.Contains(s.text(read), pageText) {
		t.Fatalf("expected the page's text; %s. Got: %s", seatsHint, s.text(read))
	}

	failed := s.call("browser_run", map[string]any{"program": `expect text "not on this page"`})
	if !failed.IsError {
		t.Errorf("a program that stopped at a failed step must come back as an error, got: %s", s.text(failed))
	}
}

// Two editor windows run two servers. Each must drive a page of its own: one
// agent's navigation must never replace what the other is looking at.
func TestTwoServersDriveTheirOwnPages(t *testing.T) {
	path, socket := riffleDaemon(t)
	first := browserServer(t, path, socket)
	second := browserServer(t, path, socket)
	url := generatedPage(t)

	opened := first.call("browser_run", map[string]any{"program": "goto " + url + "\nview read"})
	if opened.IsError || !strings.Contains(first.text(opened), pageText) {
		t.Fatalf("expected the first server to open the page; %s. Got: %s", seatsHint, first.text(opened))
	}

	// An expect rather than a view: a view replies with what changed since that
	// session's last reply, so on a shared session it can omit the very text
	// that shows the sharing.
	other := second.call("browser_run", map[string]any{"program": fmt.Sprintf("expect text %q", pageText)})
	if !other.IsError {
		t.Errorf("the second server finds the page the first one opened: two servers share one browser session")
	}

	// Not finding the page only means something from a server that works: a
	// broken relay would fail the expect above just the same.
	own := second.call("browser_run", map[string]any{"program": "goto " + url + "\nview read"})
	if own.IsError || !strings.Contains(second.text(own), pageText) {
		t.Fatalf("expected the second server to open the page itself; %s. Got: %s", seatsHint, second.text(own))
	}
}
