package browser

import (
	"slices"
	"testing"
)

// Windows names its variables in any case (Path, ProgramFiles(x86)) and
// matches them case-insensitively, and proxies are commonly exported in lower
// case. A case-sensitive allowlist would start Chrome with no PATH there, and
// with no proxy behind one. The original spelling has to survive, because a
// Unix child reads exactly the name it was given.
func TestTheBrowserAllowlistMatchesNamesInAnyCaseAndKeepsTheirSpelling(t *testing.T) {
	got := browserEnv([]string{
		"Path=C:\\Windows", "ProgramFiles(x86)=C:\\P", "https_proxy=http://proxy:3128",
		"aws_secret_access_key=x", "GITHUB_TOKEN=x", "NOETIVE_KEY_SECRET=x",
	})

	want := []string{"Path=C:\\Windows", "ProgramFiles(x86)=C:\\P", "https_proxy=http://proxy:3128"}
	if !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}
