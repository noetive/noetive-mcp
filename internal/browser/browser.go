// Package browser offers an agent a headless browser by relaying the tools of a
// riffle process it starts on this machine.
//
// riffle is a separate program with its own tools, so nothing here describes
// what they do: the tool list is read from the process once, at startup, and
// each call is passed through unchanged. That keeps the tool array identical for
// the whole session, and means a riffle that adds an argument needs no change
// here.
//
// The process and Chrome are the operator's to install. When riffle cannot be
// found the server simply offers no browser tools.
package browser

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

// EnvBin names the riffle executable to start, for an install that is not on
// PATH. It is not a secret, so it has no flag-or-environment asymmetry to
// explain: it is simply read from the environment like the rest.
const EnvBin = "NOETIVE_RIFFLE_BIN"

// EnvNames lists every environment variable this package reads, so the
// packaging emitter can check server.json against it rather than keeping a
// second list that drifts.
func EnvNames() []string { return []string{EnvBin} }

// command is the executable looked up on PATH when EnvBin is unset.
const command = "riffle"

// toolPrefix is the namespace every relayed tool must be in.
const toolPrefix = "browser_"

// startBudget bounds launching the process and listing its tools. riffle starts
// its browser lazily, on the first call, so this only has to cover process
// start; it is not the time a page takes to load.
const startBudget = 15 * time.Second

// stderrGrace bounds how long stopping the process waits for the last of its
// stderr to be written once it has exited.
const stderrGrace = 5 * time.Second

// Session is a running riffle process and the tools it offers. It is safe for
// concurrent use.
type Session struct {
	client *client.Client
	tools  []mcp.Tool
}

// Locate decides which executable to start.
//
// An explicitly named one is returned as given and is required to work: someone
// who set EnvBin chose this browser, and quietly serving without it would hide
// that their choice was not honoured. Otherwise riffle is looked for on PATH,
// and not finding it is the ordinary case of an operator who never installed
// it, reported as an empty path rather than an error. A match that is only
// relative to the working directory is treated as not found: it would run
// whatever the editor happened to be launched next to.
func Locate(getenv func(string) string, lookPath func(string) (string, error)) (path string, explicit bool) {
	if named := getenv(EnvBin); named != "" {
		return named, true
	}
	found, err := lookPath(command)
	if err != nil {
		return "", false
	}
	return found, false
}

// Start launches the executable with the `mcp` verb and lists its tools.
//
// path must be absolute, so what runs does not depend on the directory the
// editor happened to be launched from. The process gets only the variables a
// browser uses (see browserEnv), which include riffle's own site secrets and
// exclude the Noetive key and anything else the editor's shell exported.
// extraEnv is added after.
//
// What the process writes to stderr goes to this server's log, never to the
// agent: it is where riffle names the browser session it opened, so an operator
// can look at the page with `riffle view -s`, and where it says why it failed.
func Start(ctx context.Context, path string, extraEnv []string, args ...string) (*Session, error) {
	ctx, cancel := context.WithTimeout(ctx, startBudget)
	defer cancel()

	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("%q is not an absolute path", path)
	}

	c, err := client.NewStdioMCPClientWithOptions(path, extraEnv, append([]string{"mcp"}, args...), transport.WithCommandFunc(spawn))
	if err != nil {
		return nil, fmt.Errorf("start %s: %w", path, err)
	}

	initialize := mcp.InitializeRequest{}
	initialize.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initialize.Params.ClientInfo = mcp.Implementation{Name: "noetive-mcp", Version: "1"}
	if _, err := c.Initialize(ctx, initialize); err != nil {
		return nil, errors.Join(fmt.Errorf("%s did not complete the MCP handshake: %w", path, err), c.Close())
	}

	listed, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		return nil, errors.Join(fmt.Errorf("%s did not list its tools: %w", path, err), c.Close())
	}

	// Only the browser_ namespace is relayed. AddTool replaces by name, so a tool
	// the process called noetive_publish would otherwise take the place of the
	// real one and receive what the agent publishes.
	var offered []mcp.Tool
	for _, tool := range listed.Tools {
		if !strings.HasPrefix(tool.Name, toolPrefix) {
			log.Printf("not offering the browser tool %q: browser tools must be named %s*", tool.Name, toolPrefix)
			continue
		}
		offered = append(offered, tool)
	}

	return &Session{client: c, tools: offered}, nil
}

// spawn builds the process with an environment reduced to what a browser needs
// and its stderr going to this server's log.
func spawn(ctx context.Context, path string, extraEnv, args []string) (*exec.Cmd, error) {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = append(browserEnv(os.Environ()), extraEnv...)
	// Written straight to the log rather than through the transport, which
	// drops output it cannot keep up with and discards what is left when it
	// closes: exactly the lines a riffle that fails at startup says why in.
	cmd.Stderr = log.Writer()
	cmd.WaitDelay = stderrGrace
	return cmd, nil
}

// browserEnv keeps the variables riffle and the Chrome it starts need, and
// drops the rest.
//
// An editor's environment carries whatever the user's shell exports: cloud
// credentials, tokens, the Noetive key. A browser that opens pages an agent
// names has no use for any of them, so it is given a list of what it does use
// rather than this one's environment minus what we thought to remove. A
// variable riffle starts reading later has to be added here.
//
//	browserEnv([]string{"HOME=/h", "AWS_SECRET_ACCESS_KEY=x"}) // => ["HOME=/h"]
func browserEnv(environ []string) []string {
	kept := make([]string, 0, len(environ))
	for _, kv := range environ {
		name, _, _ := strings.Cut(kv, "=")
		if browserVariable(strings.ToUpper(name)) {
			kept = append(kept, kv)
		}
	}
	return kept
}

// browserVariable says whether a variable, named in upper case so Windows'
// case-insensitive names match, is one a browser uses.
func browserVariable(name string) bool {
	for _, prefix := range browserVariablePrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	_, ok := browserVariables[name]
	return ok
}

// browserVariablePrefixes are riffle's own settings and site secrets (RIFFLE_),
// locale (LC_) and the desktop session's directories (XDG_).
var browserVariablePrefixes = []string{"RIFFLE_", "LC_", "XDG_"}

// browserVariables are where to find programs, the home, temporary and cache
// directories, the locale, the display, the proxy and the certificates, on
// Unix and on Windows.
var browserVariables = map[string]struct{}{
	"PATH": {}, "HOME": {}, "USER": {}, "LOGNAME": {}, "SHELL": {}, "TMPDIR": {}, "TMP": {}, "TEMP": {},
	"LANG": {}, "LANGUAGE": {}, "TZ": {}, "DISPLAY": {}, "WAYLAND_DISPLAY": {}, "XAUTHORITY": {}, "DBUS_SESSION_BUS_ADDRESS": {},
	"FONTCONFIG_PATH": {}, "FONTCONFIG_FILE": {},
	"HTTP_PROXY": {}, "HTTPS_PROXY": {}, "NO_PROXY": {}, "ALL_PROXY": {}, "SSL_CERT_FILE": {}, "SSL_CERT_DIR": {},
	"SYSTEMROOT": {}, "WINDIR": {}, "SYSTEMDRIVE": {}, "COMSPEC": {}, "PATHEXT": {}, "USERPROFILE": {}, "USERNAME": {},
	"HOMEDRIVE": {}, "HOMEPATH": {}, "APPDATA": {}, "LOCALAPPDATA": {}, "PROGRAMDATA": {},
	"PROGRAMFILES": {}, "PROGRAMFILES(X86)": {}, "PROGRAMW6432": {}, "ALLUSERSPROFILE": {}, "PUBLIC": {}, "LD_LIBRARY_PATH": {}, "NUMBER_OF_PROCESSORS": {}, "PROCESSOR_ARCHITECTURE": {},
}

// ToolNames lists the tools the process offered.
func (s *Session) ToolNames() []string {
	names := make([]string, 0, len(s.tools))
	for _, t := range s.tools {
		names = append(names, t.Name)
	}
	return names
}

// Register adds the process's tools to srv, each relaying to it.
func (s *Session) Register(srv *mcpserver.MCPServer) {
	for _, tool := range s.tools {
		srv.AddTool(tool, s.relay)
	}
}

// Close stops the process.
func (s *Session) Close() error { return s.client.Close() }

// relay passes a call to the process and returns its answer untouched, content
// and structured result both, so the agent sees exactly what riffle said,
// including whether a program stopped at a failed step.
//
// A failure is told apart by whether the transport is gone. A live process that
// rejected the call gets a message that allows a retry; a dead one gets the
// instruction to restart, since retrying the same call would only repeat the
// failure. The underlying error goes to stderr rather than into the
// result: it can carry text from a page, and an error is the one place an agent
// reads as the server talking.
func (s *Session) relay(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	result, err := s.client.CallTool(ctx, mcp.CallToolRequest{Params: request.Params})
	if err == nil {
		return result, nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	log.Printf("browser call %s failed: %v", request.Params.Name, err)

	if !errors.Is(err, transport.ErrTransportClosed) && !errors.Is(err, io.EOF) {
		return mcp.NewToolResultError("The browser rejected this call. Check the arguments against the tool's schema and retry."), nil
	}
	return mcp.NewToolResultError("The browser (riffle) stopped responding. Do not retry this call. Restart the editor to launch it again, and run `riffle doctor` in a terminal if it keeps stopping."), nil
}
