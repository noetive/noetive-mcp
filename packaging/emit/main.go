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
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"

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
	}
	for _, e := range emitters {
		if err := e.fn(model, root); err != nil {
			log.Fatalf("%s: %v", e.name, err)
		}
	}

	fmt.Println("emitted packaging/claude-plugin, packaging/kiro-power and the repository plugin manifest")
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
	DisplayName string     `yaml:"displayName"`
	Description string     `yaml:"description"`
	Homepage    string     `yaml:"homepage"`
	Repository  string     `yaml:"repository"`
	License     string     `yaml:"license"`
	Version     string     `yaml:"version"`
	Keywords    []string   `yaml:"keywords"`
	Tools       []toolDoc  `yaml:"tools"`
	Skills      []document `yaml:"skills"`
	Steering    []document `yaml:"steering"`
}

type toolDoc struct {
	Name    string `yaml:"name"`
	Summary string `yaml:"summary"`
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
func (a authoring) serverEntry() map[string]any {
	entry := map[string]any{
		"command": a.Server.Command,
		"args":    a.Server.Args,
	}
	if len(a.Server.Env) > 0 {
		entry["env"] = a.Server.Env
	}
	return map[string]any{"mcpServers": map[string]any{a.Name: entry}}
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

func writeJSON(path string, value any) error {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(path, string(encoded)+"\n")
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
