package cleanup

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSessionOnlyCleanupKillsOrphanedTaskSession(t *testing.T) {
	t.Setenv("TMUX_PANE", "")
	directory := t.TempDir()
	log := filepath.Join(directory, "tmux.log")
	missing := filepath.Join(directory, "missing-worktree")
	tmux := filepath.Join(directory, "tmux")
	script := `#!/bin/sh
if [ "$1" = list-panes ]; then
  printf '%s\n' '$9\037stale-task\037task-id\037` + missing + `\037/repo\037ws-test\037task\037@9\037fx\037'
  exit 0
fi
if [ "$1" = kill-session ]; then
  printf '%s\n' "$3" >> '` + log + `'
  exit 0
fi
exit 1
`
	if err := os.WriteFile(tmux, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	service := &Service{Git: "git", Tmux: tmux}
	plan := service.PlanSessionCleanup(context.Background(), "$9")
	if !plan.Eligible {
		t.Fatalf("plan blocked: %#v", plan)
	}
	if err := service.ExecuteSessionCleanup(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "$9") {
		t.Fatalf("session not killed: %s", content)
	}
}
