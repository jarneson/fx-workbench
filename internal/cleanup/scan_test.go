package cleanup

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/jarneson/fx-workbench/internal/workload"
)

func TestScanWorktreesCategorizesSafeAndUnmapped(t *testing.T) {
	t.Setenv("TMUX_PANE", "")
	root, worktree := cleanupRepository(t)
	service := &Service{Git: "git", Tmux: fakeTmux(t, filepath.Join(t.TempDir(), "tmux.log"), worktree, "ship-task")}
	task := workload.Entity{ID: "ship-task", Status: "merged", Repository: root, Branch: "ship-task"}

	scan, err := service.ScanWorktrees(context.Background(), root, []workload.Entity{task})
	if err != nil {
		t.Fatal(err)
	}
	if len(scan) != 1 || scan[0].Category != CategorySafe || scan[0].TaskID != "ship-task" {
		t.Fatalf("unexpected safe scan: %#v", scan)
	}
	scan, err = service.ScanWorktrees(context.Background(), root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(scan) != 1 || scan[0].Category != CategoryBlocked {
		t.Fatalf("unexpected unmapped scan: %#v", scan)
	}
}

func TestScanWorktreesCategorizesTrustRequiredAsReview(t *testing.T) {
	t.Setenv("TMUX_PANE", "")
	root, worktree := cleanupRepository(t)
	if err := os.WriteFile(filepath.Join(worktree, "unverified.txt"), []byte("unverified\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, worktree, "add", "unverified.txt")
	runGit(t, worktree, "commit", "-qm", "conflict-resolved elsewhere")
	service := &Service{Git: "git", Tmux: fakeTmux(t, filepath.Join(t.TempDir(), "tmux.log"), worktree, "ship-task")}
	task := workload.Entity{ID: "ship-task", Status: "merged", Repository: root, Branch: "ship-task"}

	scan, err := service.ScanWorktrees(context.Background(), root, []workload.Entity{task})
	if err != nil {
		t.Fatal(err)
	}
	if len(scan) != 1 || scan[0].Category != CategoryReview {
		t.Fatalf("unexpected review scan: %#v", scan)
	}
}

func TestScanWorktreesExcludesMainWorktree(t *testing.T) {
	root := t.TempDir()
	if output, err := exec.Command("git", "init", "-q", "-b", "main", root).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	service := &Service{Git: "git", Tmux: "tmux"}
	scan, err := service.ScanWorktrees(context.Background(), root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(scan) != 0 {
		t.Fatalf("main worktree included: %#v", scan)
	}
}
