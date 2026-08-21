// Command emit generates both plugin payloads from tools/manifest.yaml.
//
// The Claude Code plugin and the Kiro Power describe the same product in two
// schemas that disagree on specifics: one expects .mcp.json and may declare
// component paths, the other expects mcp.json and rejects unknown top-level
// fields. Maintained by hand the two drift, and the drift stays invisible until
// someone installs the format nobody checked. Emitting both from one source
// makes that impossible.
//
//	go run ./packaging/emit
package main

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"log"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	json "github.com/goccy/go-json"
	"gopkg.in/yaml.v3"

	"github.com/noetive/noetive-mcp/internal/mcpserver"
)

func main() {
	log.SetFlags(0)
	log.SetPrefix("emit: ")

	root, err := repoRoot()
	if err != nil {
		log.Fatal(err)
	}

	model, err := load(filepath.Join(root, "tools"))
	if err != nil {
		log.Fatal(err)
	}

	// The manifest documents a tool surface and the server registers one. If
	// they disagree, the generated docs promise something no editor can call.
	if err := model.agreesWithServer(); err != nil {
		log.Fatal(err)
	}

	emitters := []struct {
		fn   func(authoring, string) error
		name string
	}{
		{emitClaudePlugin, "claude plugin"},
		{emitKiroPower, "kiro power"},
		{emitRepositoryPlugin, "repository plugin"},
		{emitInstall, "install links"},
	}
	for _, e := range emitters {
		if err := e.fn(model, root); err != nil {
			log.Fatalf("%s: %v", e.name, err)
		}
	}

	fmt.Println("emitted packaging/claude-plugin, packaging/kiro-power, packaging/install.json and the repository plugin manifest")
}

// authoring is the format-neutral model both emitters consume.
type authoring struct {
	Author struct {
		Name string `yaml:"name"`
		URL  string `yaml:"url"`
	} `yaml:"author"`
	Server struct {
		Env     map[string]string `yaml:"env"`
		Command string            `yaml:"command"`
		Args    []string          `yaml:"args"`
	} `yaml:"server"`
	prompts map[string]string

	Name        string     `yaml:"name"`
	ServerKey   string     `yaml:"serverKey"`
	DisplayName string     `yaml:"displayName"`
	Description string     `yaml:"description"`
	Homepage    string     `yaml:"homepage"`
	Repository  string     `yaml:"repository"`
	License     string     `yaml:"license"`
	Version     string     `yaml:"version"`
	Keywords    []string   `yaml:"keywords"`
	Tools       []toolDoc  `yaml:"tools"`
	Deeplinks   []deeplink `yaml:"deeplinks"`
	Skills      []document `yaml:"skills"`
	Steering    []document `yaml:"steering"`
}

// deeplink is a one-click install for a single editor.
type deeplink struct {
	EntryExtras map[string]any `yaml:"entryExtras"`
	Client      string         `yaml:"client"`
	Label       string         `yaml:"label"`
	URL         string         `yaml:"url"`
}

type toolDoc struct {
	Name    string `yaml:"name" json:"name"`
	Summary string `yaml:"summary" json:"summary"`
}

type document struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Source      string `yaml:"source"`
}

// load reads the manifest and every prompt body it references. A referenced
// file that is missing is fatal: emitting a skill with no instructions would
// produce a plugin that installs cleanly and does nothing.
func load(dir string) (authoring, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.yaml"))
	if err != nil {
		return authoring{}, err
	}

	var model authoring
	if err := yaml.Unmarshal(raw, &model); err != nil {
		return authoring{}, fmt.Errorf("manifest.yaml: %w", err)
	}
	if model.Name == "" || model.Description == "" {
		return authoring{}, fmt.Errorf("manifest.yaml must set name and description; both formats require them")
	}
	// An empty serverKey would emit an entry under "", which every editor reads
	// as a server with no name rather than as a mistake.
	if model.ServerKey == "" {
		return authoring{}, fmt.Errorf("manifest.yaml must set serverKey; it is the key users see in their editor config")
	}

	model.prompts = map[string]string{}
	for _, doc := range append(append([]document{}, model.Skills...), model.Steering...) {
		body, err := os.ReadFile(filepath.Join(dir, "prompts", doc.Source))
		if err != nil {
			return authoring{}, fmt.Errorf("%s references prompts/%s: %w", doc.Name, doc.Source, err)
		}
		model.prompts[doc.Source] = string(body)
	}

	return model, nil
}

// agreesWithServer checks the documented tools against the registered ones.
func (a authoring) agreesWithServer() error {
	documented := make([]string, 0, len(a.Tools))
	for _, t := range a.Tools {
		documented = append(documented, t.Name)
	}
	registered := mcpserver.ToolNames()

	sort.Strings(documented)
	sort.Strings(registered)

	if len(documented) != len(registered) {
		return fmt.Errorf("manifest documents %v but the server registers %v", documented, registered)
	}
	for i := range documented {
		if documented[i] != registered[i] {
			return fmt.Errorf("manifest documents %v but the server registers %v", documented, registered)
		}
	}
	return nil
}

// serverEntry is the mcpServers value both formats embed.
//
// Keyed on ServerKey, not Name: the plugin is called noetive-mcp but the config
// entry is called noetive, which is what installer/src/manifest/clients.json
// writes and what the README documents. Keying it on the plugin name gives a
// user who installs the plugin and also runs `init --client claude-code` two
// entries and two server processes.
func (a authoring) serverEntry() map[string]any {
	entry := map[string]any{
		"command": a.Server.Command,
		"args":    a.Server.Args,
	}
	if len(a.Server.Env) > 0 {
		entry["env"] = a.Server.Env
	}
	return map[string]any{"mcpServers": map[string]any{a.ServerKey: entry}}
}

// emitClaudePlugin writes packaging/claude-plugin.
//
// The Claude schema allows component-path fields and expects the MCP file to be
// .mcp.json — with the leading dot, unlike the Agent Plugins spec.
func emitClaudePlugin(a authoring, root string) error {
	dir := filepath.Join(root, "packaging", "claude-plugin")
	if err := reset(dir); err != nil {
		return err
	}

	manifest := map[string]any{
		"name":        a.Name,
		"displayName": a.DisplayName,
		"description": a.Description,
		"version":     a.Version,
		"author":      map[string]string{"name": a.Author.Name, "url": a.Author.URL},
		"homepage":    a.Homepage,
		"repository":  a.Repository,
		"license":     a.License,
		"keywords":    a.Keywords,
		"mcpServers":  "./.mcp.json",
	}
	if err := writeJSON(filepath.Join(dir, ".claude-plugin", "plugin.json"), manifest); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(dir, ".mcp.json"), a.serverEntry()); err != nil {
		return err
	}

	return a.writeSkills(filepath.Join(dir, "skills"))
}

// emitKiroPower writes packaging/kiro-power.
//
// The Agent Plugins plugin.json is a closed schema: an unknown top-level field
// is a validation error, not an ignored extra. Anything Kiro-specific therefore
// goes under dev.kiro/ and nowhere else, and the MCP file is mcp.json without a
// leading dot.
func emitKiroPower(a authoring, root string) error {
	dir := filepath.Join(root, "packaging", "kiro-power")
	if err := reset(dir); err != nil {
		return err
	}

	manifest := map[string]any{
		"$schema":     "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json",
		"name":        a.Name,
		"version":     a.Version,
		"description": a.Description,
		"author":      map[string]string{"name": a.Author.Name, "url": a.Author.URL},
		"keywords":    a.Keywords,
		"homepage":    a.Homepage,
		"repository":  a.Repository,
		"license":     a.License,
	}
	if err := writeJSON(filepath.Join(dir, "plugin.json"), manifest); err != nil {
		return err
	}

	server := a.serverEntry()
	server["$schema"] = "https://agent-plugins.org/schemas/1.0.0/mcp.schema.json"
	if err := writeJSON(filepath.Join(dir, "mcp.json"), server); err != nil {
		return err
	}

	if err := a.writeSkills(filepath.Join(dir, "skills")); err != nil {
		return err
	}

	for _, doc := range a.Steering {
		body := a.prompts[doc.Source]
		if err := writeFile(filepath.Join(dir, "dev.kiro", "steering", doc.Name+".md"), body); err != nil {
			return err
		}
	}
	return nil
}

// emitRepositoryPlugin refreshes the plugin manifest at the repository root.
//
// The repository is its own Claude marketplace, so the root files are what a
// user installing from GitHub actually gets. Generating them from the same
// source is what stops the marketplace copy and the packaged copy diverging.
func emitRepositoryPlugin(a authoring, root string) error {
	manifest := map[string]any{
		"name":        a.Name,
		"displayName": a.DisplayName,
		"description": a.Description,
		"version":     a.Version,
		"author":      map[string]string{"name": a.Author.Name, "url": a.Author.URL},
		"homepage":    a.Homepage,
		"repository":  a.Repository,
		"license":     a.License,
		"keywords":    a.Keywords,
	}
	if err := writeJSON(filepath.Join(root, ".claude-plugin", "plugin.json"), manifest); err != nil {
		return err
	}

	marketplace := map[string]any{
		"name":     "noetive",
		"metadata": map[string]string{"description": a.Description},
		"owner":    map[string]string{"name": a.Author.Name},
		"plugins": []map[string]any{{
			"name":        a.Name,
			"source":      "./",
			"description": a.Description,
			"author":      map[string]string{"name": a.Author.Name},
			"repository":  a.Repository,
			"license":     a.License,
			"keywords":    a.Keywords,
			"category":    "integration",
		}},
	}
	if err := writeJSON(filepath.Join(root, ".claude-plugin", "marketplace.json"), marketplace); err != nil {
		return err
	}

	if err := a.writeSkills(filepath.Join(root, "skills")); err != nil {
		return err
	}

	// Written last, after everything it points at exists. A plugin whose MCP
	// entry lands before its skills is briefly installable and broken.
	return writeJSON(filepath.Join(root, ".mcp.json"), a.serverEntry())
}

// clientManifest is the part of installer/src/manifest/clients.json this
// command needs. That file is the authority on which editors are supported and
// what they are called; re-declaring the list here would give the installer and
// the published install instructions two answers to the same question.
type clientManifest struct {
	Clients map[string]struct {
		DisplayName string `json:"displayName"`
	} `json:"clients"`
	ServerName  string `json:"serverName"`
	PackageName string `json:"packageName"`
}

// installTarget is one editor's published way in.
type installTarget struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	Command     string `json:"command"`
	Label       string `json:"label,omitempty"`
	Deeplink    string `json:"deeplink,omitempty"`
}

// emitInstall writes packaging/install.json: every editor, its install command,
// and its one-click link where one exists.
//
// It is the join of the two manifests that already exist — the client list in
// installer/src/manifest/clients.json and the server entry in
// tools/manifest.yaml — because the install instructions are the one artefact
// that needs both, and they are published in three places that have already
// drifted apart once.
func emitInstall(a authoring, root string) error {
	clients, err := loadClients(root)
	if err != nil {
		return err
	}

	// The two manifests describe the same server. Disagreeing means the plugin
	// installs one thing and the published command installs another, under two
	// keys, leaving the user with two server processes.
	if clients.ServerName != a.ServerKey {
		return fmt.Errorf("clients.json writes under %q but the manifest's serverKey is %q", clients.ServerName, a.ServerKey)
	}
	if !slices.Contains(a.Server.Args, clients.PackageName) {
		return fmt.Errorf("clients.json installs %q but the manifest runs %v", clients.PackageName, a.Server.Args)
	}

	links := make(map[string]deeplink, len(a.Deeplinks))
	for _, link := range a.Deeplinks {
		if _, known := clients.Clients[link.Client]; !known {
			return fmt.Errorf("deeplink names client %q, which clients.json does not support", link.Client)
		}
		links[link.Client] = link
	}

	targets := make([]installTarget, 0, len(clients.Clients))
	for _, id := range slices.Sorted(maps.Keys(clients.Clients)) {
		target := installTarget{
			ID:          id,
			DisplayName: clients.Clients[id].DisplayName,
			Command:     fmt.Sprintf("npx %s init --client %s", clients.PackageName, id),
		}
		if link, ok := links[id]; ok {
			url, err := a.expandDeeplink(link)
			if err != nil {
				return err
			}
			target.Label, target.Deeplink = link.Label, url
		}
		targets = append(targets, target)
	}

	return writeJSON(filepath.Join(root, "packaging", "install.json"), map[string]any{
		"serverKey":   a.ServerKey,
		"packageName": clients.PackageName,
		"registry":    "io.noetive/mcp-server",
		"tools":       a.Tools,
		"clients":     targets,
	})
}

// expandDeeplink fills a link template with the server entry it installs.
//
// The entry is the same one the plugin formats embed, so a change to the
// command or its arguments reaches the buttons without anyone editing a URL.
func (a authoring) expandDeeplink(link deeplink) (string, error) {
	entry := map[string]any{"command": a.Server.Command, "args": a.Server.Args}
	if len(a.Server.Env) > 0 {
		entry["env"] = a.Server.Env
	}
	for key, value := range link.EntryExtras {
		entry[key] = value
	}

	encoded, err := json.Marshal(entry)
	if err != nil {
		return "", err
	}

	replacer := strings.NewReplacer(
		"${serverKey}", url.QueryEscape(a.ServerKey),
		"${config}", url.QueryEscape(string(encoded)),
		"${configBase64}", base64.StdEncoding.EncodeToString(encoded),
	)
	expanded := replacer.Replace(link.URL)

	// An unreplaced placeholder produces a link that opens the editor and then
	// installs nothing, which looks like the editor's fault.
	if strings.Contains(expanded, "${") {
		return "", fmt.Errorf("%s deeplink has an unknown placeholder: %s", link.Client, link.URL)
	}
	return expanded, nil
}

// loadClients reads the installer's client manifest.
func loadClients(root string) (clientManifest, error) {
	path := filepath.Join(root, "installer", "src", "manifest", "clients.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return clientManifest{}, err
	}

	var manifest clientManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return clientManifest{}, fmt.Errorf("%s: %w", path, err)
	}
	if len(manifest.Clients) == 0 {
		return clientManifest{}, fmt.Errorf("%s lists no clients", path)
	}
	return manifest, nil
}

func (a authoring) writeSkills(dir string) error {
	for _, doc := range a.Skills {
		body := a.prompts[doc.Source]
		front := fmt.Sprintf("---\nname: %s\ndescription: %s\n---\n\n", doc.Name, doc.Description)
		if err := writeFile(filepath.Join(dir, doc.Name, "SKILL.md"), front+body); err != nil {
			return err
		}
	}
	return nil
}

// reset clears a generated directory so a removed entry actually disappears
// rather than lingering from an earlier run.
func reset(dir string) error {
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	return os.MkdirAll(dir, 0o755)
}

// writeJSON emits a generated JSON file.
//
// HTML escaping is off because these files are read by editors and by
// noetive.io, never embedded in a <script> tag. Left on, the & in every install
// deeplink becomes & — which parses back correctly but makes a generated
// URL unreadable in review, exactly where a wrong one has to be spotted.
func writeJSON(path string, value any) error {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")

	if err := encoder.Encode(value); err != nil {
		return err
	}
	return writeFile(path, buffer.String())
}

func writeFile(path, contents string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(contents), 0o644)
}

// repoRoot walks up from the working directory to the module root, so the
// command works from anywhere in the tree rather than only from the top.
func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return moduleRootAbove(dir)
}

// moduleRootAbove finds the nearest ancestor of start holding a go.mod.
//
// Separated from repoRoot so the walk itself can be tested. It decides where
// every generated file is written, and a wrong answer scatters a plugin payload
// somewhere in the tree — or, worse, into the wrong repository — with a
// successful exit status either way.
func moduleRootAbove(start string) (string, error) {
	dir := start
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}

		parent := filepath.Dir(dir)
		// filepath.Dir of a filesystem root returns the root itself, which is
		// the only way this loop can terminate without finding anything.
		if parent == dir {
			return "", fmt.Errorf("no go.mod found above %s", start)
		}
		dir = parent
	}
}
