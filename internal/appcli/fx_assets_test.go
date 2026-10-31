package appcli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestFXInstallDryRunUsesEmbeddedBundle(t *testing.T) {
	target := filepath.Join(t.TempDir(), "fx")
	var stdout, stderr bytes.Buffer
	code := Run([]string{"fx", "install", "--dry-run", "--target", target}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	for _, expected := range []string{
		"fx root: " + target,
		"would install: skills/obsidian-planner/SKILL.md",
		"would install: skills/worktree-task-launcher/SKILL.md",
	} {
		if !strings.Contains(stdout.String(), expected) {
			t.Errorf("output missing %q:\n%s", expected, stdout.String())
		}
	}
}
