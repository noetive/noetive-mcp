package main

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	json "github.com/goccy/go-json"
	"gopkg.in/yaml.v3"

	"github.com/noetive/noetive-mcp/internal/mcpserver"
)

// authoringSource writes a minimal but complete authoring tree and returns its
// directory, so each test starts from a known-good manifest and mutates one
// thing about it.
func authoringSource(t *testing.T, manifest string) string {
	t.Helper()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "manifest.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("could not write manifest: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "prompts"), 0o755); err != nil {
		t.Fatalf("could not create prompts: %v", err)
	}
	for _, name := range []string{"doctor.md", "using-noetive.md"} {
		if err := os.WriteFile(filepath.Join(dir, "prompts", name), []byte("body of "+name), 0o644); err != nil {
			t.Fatalf("could not write %s: %v", name, err)
		}
	}
	return dir
}

// completeManifest lists exactly the tools the server registers, so it passes
// the drift check unless a test deliberately changes that.
func completeManifest(t *testing.T) string {
	t.Helper()

	var tools strings.Builder
	for _, name := range mcpserver.ToolNames() {
		tools.WriteString("  - name: " + name + "\n    summary: does a thing\n")
	}

	return `name: noetive-mcp
serverKey: noetive
displayName: Noetive
description: Connect your AI agent to the Noetive Semantik semantic broker
author:
  name: Noetive.io
  url: https://noetive.io
homepage: https://noetive.io/mcp
repository: https://github.com/noetive/noetive-mcp
license: ISC
version: 9.9.9
keywords:
  - noetive
server:
  command: npx
  args: ["-y", "@noetive/mcp-server"]
  env:
    NOETIVE_KEY_SECRET: "${NOETIVE_KEY_SECRET}"
tools:
` + tools.String() + `skills:
  - name: doctor
    description: Run diagnostics
    source: doctor.md
steering:
  - name: using-noetive
    source: using-noetive.md
`
}

// withExtraTool adds a tool to the tools list specifically, rather than to the
// end of the document where YAML would fold it into the last list it saw.
func withExtraTool(t *testing.T, name string) string {
	t.Helper()

	entry := "  - name: " + name + "\n    summary: not registered\n"
	manifest := strings.Replace(completeManifest(t), "skills:\n", entry+"skills:\n", 1)
	if !strings.Contains(manifest, name) {
		t.Fatal("the manifest template changed shape; the tools list marker no longer matches")
	}
	return manifest
}

// The whole reason this generator exists: the documented tool surface and the
// registered one must not drift. A manifest that promises a tool no editor can
// call produces plugins that install cleanly and mislead.
func TestDriftBetweenManifestAndServerIsRefused(t *testing.T) {
	manifest := withExtraTool(t, "noetive_imaginary")
	model, err := load(authoringSource(t, manifest))
	if err != nil {
		t.Fatalf("load returned error: %v", err)
	}

	err = model.agreesWithServer()
	if err == nil {
		t.Fatal("expected a manifest documenting an unregistered tool to be refused")
	}
	if !strings.Contains(err.Error(), "noetive_imaginary") {
		t.Errorf("expected the error to name the offending tool, got: %v", err)
	}
}

// The mirror case: a tool registered but left undocumented would ship a plugin
// whose documentation is silently incomplete.
func TestAnUndocumentedRegisteredToolIsRefused(t *testing.T) {
	manifest := strings.Replace(completeManifest(t), "  - name: "+mcpserver.ToolNames()[0]+"\n    summary: does a thing\n", "", 1)

	model, err := load(authoringSource(t, manifest))
	if err != nil {
		t.Fatalf("load returned error: %v", err)
	}

	if err := model.agreesWithServer(); err == nil {
		t.Fatal("expected a manifest missing a registered tool to be refused")
	}
}

func TestAMatchingManifestIsAccepted(t *testing.T) {
	model, err := load(authoringSource(t, completeManifest(t)))
	if err != nil {
		t.Fatalf("load returned error: %v", err)
	}

	if err := model.agreesWithServer(); err != nil {
		t.Errorf("expected a matching manifest to be accepted, got: %v", err)
	}
}

// A referenced prompt that does not exist must stop the build. Emitting a skill
// with no instructions produces a plugin that installs cleanly and does nothing.
func TestAMissingPromptStopsTheBuild(t *testing.T) {
	dir := authoringSource(t, completeManifest(t))
	if err := os.Remove(filepath.Join(dir, "prompts", "doctor.md")); err != nil {
		t.Fatalf("could not remove the prompt: %v", err)
	}

	_, err := load(dir)
	if err == nil {
		t.Fatal("expected a missing prompt body to be refused")
	}
	if !strings.Contains(err.Error(), "doctor.md") {
		t.Errorf("expected the error to name the missing file, got: %v", err)
	}
}

// Both plugin formats require a name and a description. Catching it here names
// the manifest; letting it through produces a schema error from a vendor tool
// that says nothing about where the value should have come from.
func TestAManifestMissingRequiredFieldsIsRefused(t *testing.T) {
	if _, err := load(authoringSource(t, "version: 1.0.0\n")); err == nil {
		t.Fatal("expected a manifest with no name or description to be refused")
	}
}

// The two formats disagree on the MCP filename — .mcp.json with a leading dot
// for Claude, mcp.json without for Agent Plugins. Emitting the wrong one
// produces a plugin the host silently ignores.
func TestEachFormatGetsItsOwnMCPFilename(t *testing.T) {
	model, err := load(authoringSource(t, completeManifest(t)))
	if err != nil {
		t.Fatalf("load returned error: %v", err)
	}

	root := t.TempDir()
	if err := emitClaudePlugin(model, root); err != nil {
		t.Fatalf("emitClaudePlugin: %v", err)
	}
	if err := emitKiroPower(model, root); err != nil {
		t.Fatalf("emitKiroPower: %v", err)
	}

	assertExists(t, filepath.Join(root, "packaging", "claude-plugin", ".mcp.json"))
	assertExists(t, filepath.Join(root, "packaging", "kiro-power", "mcp.json"))
	assertMissing(t, filepath.Join(root, "packaging", "kiro-power", ".mcp.json"))
	assertMissing(t, filepath.Join(root, "packaging", "claude-plugin", "mcp.json"))
}

// The Agent Plugins plugin.json is a closed schema: an unknown top-level field
// is a validation error, not an ignored extra. Kiro-specific display metadata
// therefore has to go under dev.kiro and nowhere else.
func TestTheKiroManifestCarriesNoFieldsItsSchemaRejects(t *testing.T) {
	model, err := load(authoringSource(t, completeManifest(t)))
	if err != nil {
		t.Fatalf("load returned error: %v", err)
	}

	root := t.TempDir()
	if err := emitKiroPower(model, root); err != nil {
		t.Fatalf("emitKiroPower: %v", err)
	}

	manifest := readJSON(t, filepath.Join(root, "packaging", "kiro-power", "plugin.json"))

	allowed := map[string]bool{
		"$schema": true, "name": true, "version": true, "description": true,
		"author": true, "keywords": true, "homepage": true, "repository": true, "license": true,
	}
	for key := range manifest {
		if !allowed[key] {
			t.Errorf("field %q is not in the Agent Plugins schema and will fail validation", key)
		}
	}

	// displayName is Claude-only; publishing it here is the exact mistake the
	// closed schema exists to catch.
	if _, present := manifest["displayName"]; present {
		t.Error("displayName leaked into the Agent Plugins manifest")
	}
}

// Kiro activates a Power only when a keyword appears in conversation. An empty
// list makes the Power installed but permanently dormant.
func TestTheKiroManifestCarriesActivationKeywords(t *testing.T) {
	model, err := load(authoringSource(t, completeManifest(t)))
	if err != nil {
		t.Fatalf("load returned error: %v", err)
	}

	root := t.TempDir()
	if err := emitKiroPower(model, root); err != nil {
		t.Fatalf("emitKiroPower: %v", err)
	}

	manifest := readJSON(t, filepath.Join(root, "packaging", "kiro-power", "plugin.json"))
	keywords, ok := manifest["keywords"].([]any)
	if !ok || len(keywords) == 0 {
		t.Errorf("expected activation keywords, got %v", manifest["keywords"])
	}
}

// Both formats must spawn the server the same way. A format that drops -y
// leaves npx blocking on a prompt nobody can answer, in an editor with no
// terminal attached.
func TestBothFormatsSpawnTheServerIdentically(t *testing.T) {
	model, err := load(authoringSource(t, completeManifest(t)))
	if err != nil {
		t.Fatalf("load returned error: %v", err)
	}

	root := t.TempDir()
	if err := emitClaudePlugin(model, root); err != nil {
		t.Fatalf("emitClaudePlugin: %v", err)
	}
	if err := emitKiroPower(model, root); err != nil {
		t.Fatalf("emitKiroPower: %v", err)
	}

	claude := serverEntryOf(t, filepath.Join(root, "packaging", "claude-plugin", ".mcp.json"), model.ServerKey)
	kiro := serverEntryOf(t, filepath.Join(root, "packaging", "kiro-power", "mcp.json"), model.ServerKey)

	if claude["command"] != kiro["command"] {
		t.Errorf("commands differ: %v vs %v", claude["command"], kiro["command"])
	}
	args, ok := claude["args"].([]any)
	if !ok || len(args) == 0 || args[0] != "-y" {
		t.Errorf("expected npx to be invoked non-interactively, got %v", claude["args"])
	}
}

// Regenerating must remove what the manifest no longer declares. Without the
// reset, a deleted skill lingers in the emitted payload and ships forever.
func TestRegeneratingRemovesWhatTheManifestNoLongerDeclares(t *testing.T) {
	model, err := load(authoringSource(t, completeManifest(t)))
	if err != nil {
		t.Fatalf("load returned error: %v", err)
	}

	root := t.TempDir()
	if err := emitClaudePlugin(model, root); err != nil {
		t.Fatalf("first emit: %v", err)
	}

	stale := filepath.Join(root, "packaging", "claude-plugin", "skills", "retired", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(stale), 0o755); err != nil {
		t.Fatalf("could not stage a stale file: %v", err)
	}
	if err := os.WriteFile(stale, []byte("left over"), 0o644); err != nil {
		t.Fatalf("could not stage a stale file: %v", err)
	}

	if err := emitClaudePlugin(model, root); err != nil {
		t.Fatalf("second emit: %v", err)
	}

	assertMissing(t, stale)
}

// Emitting twice must produce identical bytes, or CI's "packaging is up to
// date" check fails on every run and stops meaning anything.
func TestEmittingTwiceIsByteIdentical(t *testing.T) {
	model, err := load(authoringSource(t, completeManifest(t)))
	if err != nil {
		t.Fatalf("load returned error: %v", err)
	}

	root := t.TempDir()
	if err := emitKiroPower(model, root); err != nil {
		t.Fatalf("first emit: %v", err)
	}
	first := readFile(t, filepath.Join(root, "packaging", "kiro-power", "plugin.json"))

	if err := emitKiroPower(model, root); err != nil {
		t.Fatalf("second emit: %v", err)
	}
	second := readFile(t, filepath.Join(root, "packaging", "kiro-power", "plugin.json"))

	if first != second {
		t.Error("regenerating produced different bytes from the same source")
	}
}

// The skill body has to survive into the emitted file. An empty SKILL.md
// installs cleanly and does nothing.
func TestSkillBodiesReachTheEmittedFiles(t *testing.T) {
	model, err := load(authoringSource(t, completeManifest(t)))
	if err != nil {
		t.Fatalf("load returned error: %v", err)
	}

	root := t.TempDir()
	if err := emitClaudePlugin(model, root); err != nil {
		t.Fatalf("emitClaudePlugin: %v", err)
	}

	skill := readFile(t, filepath.Join(root, "packaging", "claude-plugin", "skills", "doctor", "SKILL.md"))
	if !strings.Contains(skill, "body of doctor.md") {
		t.Errorf("expected the prompt body in the skill, got: %s", skill)
	}
	if !strings.HasPrefix(skill, "---\nname: doctor\n") {
		t.Errorf("expected skill frontmatter, got: %s", skill)
	}
}

func serverEntryOf(t *testing.T, path, name string) map[string]any {
	t.Helper()

	doc := readJSON(t, path)
	servers, ok := doc["mcpServers"].(map[string]any)
	if !ok {
		t.Fatalf("%s has no mcpServers object", path)
	}
	entry, ok := servers[name].(map[string]any)
	if !ok {
		t.Fatalf("%s has no %s entry", path, name)
	}
	return entry
}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()

	var doc map[string]any
	if err := json.Unmarshal([]byte(readFile(t, path)), &doc); err != nil {
		t.Fatalf("%s is not valid JSON: %v", path, err)
	}
	return doc
}

func readFile(t *testing.T, path string) string {
	t.Helper()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("could not read %s: %v", path, err)
	}
	return string(raw)
}

func assertExists(t *testing.T, path string) {
	t.Helper()

	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected %s to exist", path)
	}
}

func assertMissing(t *testing.T, path string) {
	t.Helper()

	if _, err := os.Stat(path); err == nil {
		t.Errorf("expected %s not to exist", path)
	}
}

// The repository is its own Claude marketplace, so the root files are what a
// user installing from GitHub actually gets. Generating them from the same
// source is what stops the marketplace copy and the packaged copy diverging.
func TestTheRepositoryPluginIsGeneratedFromTheSameSource(t *testing.T) {
	model, err := load(authoringSource(t, completeManifest(t)))
	if err != nil {
		t.Fatalf("load returned error: %v", err)
	}

	root := t.TempDir()
	if err := emitRepositoryPlugin(model, root); err != nil {
		t.Fatalf("emitRepositoryPlugin: %v", err)
	}

	plugin := readJSON(t, filepath.Join(root, ".claude-plugin", "plugin.json"))
	if plugin["version"] != model.Version {
		t.Errorf("expected version %q, got %v", model.Version, plugin["version"])
	}

	// The marketplace entry must point at this repository, or installing from
	// GitHub resolves a plugin that is not the one being published.
	marketplace := readJSON(t, filepath.Join(root, ".claude-plugin", "marketplace.json"))
	plugins, ok := marketplace["plugins"].([]any)
	if !ok || len(plugins) != 1 {
		t.Fatalf("expected exactly one marketplace plugin, got %v", marketplace["plugins"])
	}
	entry, ok := plugins[0].(map[string]any)
	if !ok || entry["source"] != "./" {
		t.Errorf("expected the marketplace entry to source this repository, got %v", plugins[0])
	}
	if entry["name"] != model.Name {
		t.Errorf("expected the marketplace entry to name %q, got %v", model.Name, entry["name"])
	}

	// The root skill is what a plugin user invokes; omitting it ships a plugin
	// whose documented command does not exist.
	assertExists(t, filepath.Join(root, "skills", "doctor", "SKILL.md"))
	assertExists(t, filepath.Join(root, ".mcp.json"))
}

// The module root decides where every generated file lands. A wrong answer
// scatters a plugin payload somewhere in the tree, or into a neighbouring
// repository, with a successful exit status either way.
func TestTheModuleRootIsFoundByWalkingUp(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test\n"), 0o644); err != nil {
		t.Fatalf("could not stage go.mod: %v", err)
	}

	nested := filepath.Join(root, "packaging", "emit")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("could not create the nested directory: %v", err)
	}

	scenarios := map[string]string{
		"from the root itself":  root,
		"from a nested package": nested,
		"from an intermediate":  filepath.Join(root, "packaging"),
	}

	for name, start := range scenarios {
		t.Run(name, func(t *testing.T) {
			got, err := moduleRootAbove(start)
			if err != nil {
				t.Fatalf("moduleRootAbove returned error: %v", err)
			}
			// Compared through EvalSymlinks because a temp directory is behind
			// a symlink on macOS, and the walk returns the path it was given.
			if resolve(t, got) != resolve(t, root) {
				t.Errorf("expected %s, got %s", root, got)
			}
		})
	}
}

// The walk has to terminate at the filesystem root rather than looping. Without
// that guard the generator hangs instead of reporting that it is being run from
// outside any module.
func TestTheWalkTerminatesWhenThereIsNoModule(t *testing.T) {
	done := make(chan error, 1)
	go func() {
		_, err := moduleRootAbove(t.TempDir())
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected an error when no go.mod exists above the start")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the walk did not terminate; it is looping at the filesystem root")
	}
}

// A write that fails must stop the run and say so. Continuing would leave a
// half-generated payload that looks complete to the next step, and CI's
// up-to-date check would then compare against a partial tree.
func TestAFailedWriteStopsTheRunRatherThanLeavingAPartialPayload(t *testing.T) {
	model, err := load(authoringSource(t, completeManifest(t)))
	if err != nil {
		t.Fatalf("load returned error: %v", err)
	}

	emitters := map[string]func(authoring, string) error{
		"claude plugin":     emitClaudePlugin,
		"kiro power":        emitKiroPower,
		"repository plugin": emitRepositoryPlugin,
	}

	for name, emit := range emitters {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()

			// A regular file where the emitter needs a directory: every write
			// beneath it fails, without depending on permissions that behave
			// differently for root or across platforms.
			blocker := filepath.Join(root, "packaging")
			if name == "repository plugin" {
				blocker = filepath.Join(root, ".claude-plugin")
			}
			if err := os.WriteFile(blocker, []byte("not a directory"), 0o644); err != nil {
				t.Fatalf("could not stage the blocker: %v", err)
			}

			if err := emit(model, root); err == nil {
				t.Error("expected the emitter to report the failed write")
			}
		})
	}
}

func resolve(t *testing.T, path string) string {
	t.Helper()

	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return path
	}
	return resolved
}

// Both fields are required, and either one missing is fatal. Requiring only the
// pair would let a manifest with a name and no description through, producing
// plugins whose listing in a marketplace is blank.
func TestEitherRequiredFieldMissingIsRefused(t *testing.T) {
	scenarios := map[string]string{
		"no description": "name: noetive-mcp\nserverKey: noetive\nversion: 1.0.0\n",
		"no name":        "description: something\nserverKey: noetive\nversion: 1.0.0\n",
		"no serverKey":   "name: noetive-mcp\ndescription: something\nversion: 1.0.0\n",
		"neither":        "version: 1.0.0\n",
	}

	for name, manifest := range scenarios {
		t.Run(name, func(t *testing.T) {
			if _, err := load(authoringSource(t, manifest)); err == nil {
				t.Error("expected the manifest to be refused")
			}
		})
	}
}

// An env block is written only when there is something in it. An empty one is
// noise in a file a user reads, and it implies a variable the server needs when
// there is none.
func TestNoEnvBlockIsWrittenWhenThereIsNothingInIt(t *testing.T) {
	manifest := strings.Replace(
		completeManifest(t),
		"  env:\n    NOETIVE_KEY_SECRET: \"${NOETIVE_KEY_SECRET}\"\n",
		"",
		1,
	)

	model, err := load(authoringSource(t, manifest))
	if err != nil {
		t.Fatalf("load returned error: %v", err)
	}

	root := t.TempDir()
	if err := emitKiroPower(model, root); err != nil {
		t.Fatalf("emitKiroPower: %v", err)
	}

	entry := serverEntryOf(t, filepath.Join(root, "packaging", "kiro-power", "mcp.json"), model.ServerKey)
	if _, present := entry["env"]; present {
		t.Errorf("an empty env block was written: %v", entry["env"])
	}
}

// A failure writing the skills has to stop the run like any other. The
// repository plugin writes them after its manifests, so a payload that fails
// here would otherwise be published with a manifest pointing at a skill that
// was never written.
func TestAFailureWritingSkillsStopsTheRun(t *testing.T) {
	model, err := load(authoringSource(t, completeManifest(t)))
	if err != nil {
		t.Fatalf("load returned error: %v", err)
	}

	root := t.TempDir()
	// A regular file where the skills directory belongs.
	if err := os.WriteFile(filepath.Join(root, "skills"), []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("could not stage the blocker: %v", err)
	}

	if err := emitRepositoryPlugin(model, root); err == nil {
		t.Error("expected the failed skill write to be reported")
	}
}

// The plugin is called noetive-mcp; the config entry it installs is called
// noetive. Keying the entry on the plugin name gives a user who installs the
// plugin and also runs `init --client claude-code` two entries and two server
// processes, which is the failure this key exists to prevent.
func TestTheServerEntryIsKeyedOnTheServerKeyNotThePluginName(t *testing.T) {
	model, err := load(authoringSource(t, completeManifest(t)))
	if err != nil {
		t.Fatalf("load returned error: %v", err)
	}

	servers, ok := model.serverEntry()["mcpServers"].(map[string]any)
	if !ok {
		t.Fatal("the entry carries no mcpServers object")
	}
	if _, found := servers[model.ServerKey]; !found {
		t.Errorf("no entry under the server key %q; got keys %v", model.ServerKey, slices.Sorted(maps.Keys(servers)))
	}
	if _, found := servers[model.Name]; found {
		t.Errorf("an entry was written under the plugin name %q, which installs a second server", model.Name)
	}
}

// The version is stamped from the git tag into installer/package.json and
// server.json at release, but tools/manifest.yaml is committed and emitted into
// the plugin manifests. Nothing at release reconciles the two, so the only
// moment the disagreement is cheap to find is here — a tag that disagrees
// publishes an npm package and a registry entry describing different builds.
func TestEveryPublishedManifestDeclaresTheSameVersion(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Fatalf("could not find the module root: %v", err)
	}

	var manifest struct {
		Version string `yaml:"version"`
	}
	readInto(t, filepath.Join(root, "tools", "manifest.yaml"), yaml.Unmarshal, &manifest)

	var pkg struct {
		Version              string            `json:"version"`
		OptionalDependencies map[string]string `json:"optionalDependencies"`
	}
	readInto(t, filepath.Join(root, "installer", "package.json"), json.Unmarshal, &pkg)

	// The lockfile is what `npm ci` reads, so a stale copy here publishes a
	// wrapper resolving the previous release's platform packages.
	var lock struct {
		Version  string `json:"version"`
		Packages map[string]struct {
			Version              string            `json:"version"`
			OptionalDependencies map[string]string `json:"optionalDependencies"`
		} `json:"packages"`
	}
	readInto(t, filepath.Join(root, "installer", "package-lock.json"), json.Unmarshal, &lock)

	var server struct {
		Version  string `json:"version"`
		Packages []struct {
			Identifier string `json:"identifier"`
			Version    string `json:"version"`
		} `json:"packages"`
	}
	readInto(t, filepath.Join(root, "server.json"), json.Unmarshal, &server)

	want := manifest.Version
	if want == "" {
		t.Fatal("tools/manifest.yaml declares no version")
	}

	declared := map[string]string{
		"installer/package.json":      pkg.Version,
		"installer/package-lock.json": lock.Version,
		"server.json":                 server.Version,
	}
	for name, version := range pkg.OptionalDependencies {
		declared["installer/package.json optionalDependencies "+name] = version
	}
	declared["installer/package-lock.json root package"] = lock.Packages[""].Version
	for name, version := range lock.Packages[""].OptionalDependencies {
		declared["installer/package-lock.json root optionalDependencies "+name] = version
	}
	for _, p := range server.Packages {
		declared["server.json packages "+p.Identifier] = p.Version
	}

	for where, got := range declared {
		if got != want {
			t.Errorf("%s declares %q but tools/manifest.yaml declares %q", where, got, want)
		}
	}
}

// readInto keeps the decodes above to one line each; the assertion is what the
// test is about, not the plumbing. The decode function is a parameter because
// one of the four files is YAML and the rest are JSON.
func readInto(t *testing.T, path string, decode func([]byte, any) error, target any) {
	t.Helper()

	if err := decode([]byte(readFile(t, path)), target); err != nil {
		t.Fatalf("could not decode %s: %v", path, err)
	}
}
