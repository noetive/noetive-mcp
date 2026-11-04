package tools_test

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"

	"go.noetive.io/noetive-mcp/tools"
)

// TestTheBudSkillNamesTheToolsTheServerServes keeps the skill and the surface
// in step. A skill that teaches a tool the server no longer registers sends the
// agent looking for it, and one that never mentions a registered tool leaves it
// unused; both read to a user as the agent not knowing its own tools.
func TestTheBudSkillNamesTheToolsTheServerServes(t *testing.T) {
	sources := []string{"prompts/bud.md"}
	refs, err := filepath.Glob("prompts/bud/*.md")
	if err != nil || len(refs) == 0 {
		t.Fatalf("no skill references found: %v", err)
	}
	sources = append(sources, refs...)

	named := map[string]bool{}
	mention := regexp.MustCompile(`\bbud_[a-z]+\b`)
	for _, path := range sources {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range mention.FindAllString(string(body), -1) {
			named[name] = true
		}
	}

	var got []string
	for name := range named {
		got = append(got, name)
	}
	slices.Sort(got)
	want := slices.Sorted(slices.Values(tools.BudToolNames()))
	if !slices.Equal(got, want) {
		t.Errorf("the bud skill names %v; the server registers %v", got, want)
	}
}
