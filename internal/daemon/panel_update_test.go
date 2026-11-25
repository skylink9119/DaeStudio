package daemon

import (
	"strings"
	"testing"
)

func TestPanelUpdateSourceAndChecksums(t *testing.T) {
	for _, input := range []string{"SKYBYTE/DaeStudio", "https://github.com/SKYBYTE/DaeStudio.git", "https://github.com/SKYBYTE/DaeStudio/releases"} {
		if repo, err := NormalizeGitHubRepo(input); err != nil || repo != "SKYBYTE/DaeStudio" {
			t.Fatalf("%s: %s, %v", input, repo, err)
		}
	}
	for _, input := range []string{"https://other.example/SKYBYTE/DaeStudio", "SKYBYTE/DaeStudio/extra", "../repo"} {
		if _, err := NormalizeGitHubRepo(input); err == nil {
			t.Fatalf("accepted %s", input)
		}
	}
	digest := strings.Repeat("a", 64)
	line := digest + "  daed-next-linux-arm64\n"
	if got, err := parseSHA256Sums(line, "daed-next-linux-arm64"); err != nil || got != digest {
		t.Fatalf("%s %v", got, err)
	}
	if _, err := parseSHA256Sums(line+line, "daed-next-linux-arm64"); err == nil {
		t.Fatal("ambiguous checksum accepted")
	}
}

func TestLatestPanelReleaseInSharedRepository(t *testing.T) {
	panel := githubPanelRelease{Tag: "DaeStudio-v1.0.0"}
	panel.Assets = append(panel.Assets, struct {
		Name   string `json:"name"`
		Digest string `json:"digest"`
	}{Name: "daed-next-linux-arm64"})
	preview := panel
	preview.Tag, preview.Prerelease = "v9.0.0", true
	release, version := latestPanelRelease([]githubPanelRelease{{Tag: "v8.0.0"}, preview, panel})
	if release.Tag != panel.Tag || version != "v1.0.0" {
		t.Fatalf("selected %q, %q", release.Tag, version)
	}
}
