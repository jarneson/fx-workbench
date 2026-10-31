package cleanup

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/jarneson/fx-workbench/internal/workload"
)

func TestInventoryListsWorktreesWithoutFullValidation(t *testing.T) {
	root, _ := cleanupRepository(t)
	service := &Service{Git: "git", Tmux: "tmux"}
	task := workload.Entity{ID: "ship-task", Status: "merged", Repository: root, Branch: "ship-task"}
	entries, err := service.Inventory(context.Background(), root, []workload.Entity{task})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Category != InventoryVerify || entries[0].TaskID != "ship-task" {
		t.Fatalf("unexpected inventory: %#v", entries)
	}
	entries, err = service.Inventory(context.Background(), root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Category != InventoryBlocked || len(entries[0].Reasons) == 0 {
		t.Fatalf("unexpected unmapped inventory: %#v", entries)
	}
}

func TestInventoryIncludesTaskSessionWhoseWorktreeIsGone(t *testing.T) {
	t.Setenv("TMUX_PANE", "")
	root, worktree := cleanupRepository(t)
	runGit(t, root, "worktree", "remove", "--force", worktree)
	directory := t.TempDir()
	tmux := filepath.Join(directory, "tmux")
	script := `#!/bin/sh
if [ "$1" = list-panes ]; then
  printf '%s\n' '$9\037stale-task\037ship-task\037` + worktree + `\037` + root + `\037ws-test\037task\037@9\037fx\037'
  exit 0
fi
exit 1
`
	if err := os.WriteFile(tmux, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	service := &Service{Git: "git", Tmux: tmux}
	task := workload.Entity{ID: "ship-task", Project: "demo", Status: "merged", Repository: root, Branch: "ship-task"}
	entries, err := service.Inventory(context.Background(), root, []workload.Entity{task})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Category != InventoryStaleSession || entries[0].TaskID != "ship-task" || entries[0].SessionID != "$9" {
		t.Fatalf("unexpected stale session inventory: %#v", entries)
	}
}

func TestInventoryBlocksNonMergedTaskWithoutGitStatus(t *testing.T) {
	root, _ := cleanupRepository(t)
	service := &Service{Git: "git", Tmux: "tmux"}
	task := workload.Entity{ID: "ship-task", Status: "done", Repository: root, Branch: "ship-task"}
	entries, err := service.Inventory(context.Background(), root, []workload.Entity{task})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Category != InventoryBlocked {
		t.Fatalf("done task unexpectedly actionable: %#v", entries)
	}
}
