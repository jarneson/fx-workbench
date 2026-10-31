package cleanup

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestForceCleanupRemovesDirtyWorktreeAndBranch(t *testing.T) {
	t.Setenv("TMUX_PANE", "")
	root, worktree := cleanupRepository(t)
	if err := os.WriteFile(filepath.Join(worktree, "uncommitted.txt"), []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	service := &Service{Git: "git", Tmux: fakeTmux(t, filepath.Join(t.TempDir(), "tmux.log"), worktree, "ship-task")}
	plan := service.PlanForce(context.Background(), root, worktree)
	if !plan.Eligible || len(plan.Changes) == 0 {
		t.Fatalf("force plan unexpectedly blocked or clean: %#v", plan)
	}
	result, err := service.ExecuteForce(context.Background(), plan, true)
	if err != nil {
		t.Fatal(err)
	}
	if !result.WorktreeRemoved || !result.BranchDeleted || result.SessionsKilled != 1 {
		t.Fatalf("unexpected force result: %#v", result)
	}
	if _, err := os.Stat(worktree); !os.IsNotExist(err) {
		t.Fatalf("worktree still exists: %v", err)
	}
}

func TestForceCleanupNeverRemovesRootWorktree(t *testing.T) {
	root, _ := cleanupRepository(t)
	service := &Service{Git: "git", Tmux: fakeTmux(t, filepath.Join(t.TempDir(), "tmux.log"), root, "")}
	plan := service.PlanForce(context.Background(), root, root)
	if plan.Eligible || !strings.Contains(strings.Join(plan.Blockers, "; "), "root worktree") {
		t.Fatalf("root worktree not blocked: %#v", plan)
	}
}
