package cleanup

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jarneson/fx-workbench/internal/workload"
)

func TestPlanAndExecutePatchEquivalentCompletedTask(t *testing.T) {
	t.Setenv("TMUX_PANE", "")
	root, worktree := cleanupRepository(t)
	log := filepath.Join(t.TempDir(), "tmux.log")
	tmux := fakeTmux(t, log, worktree, "ship-task")
	service := &Service{Git: "git", Tmux: tmux}
	task := workload.Entity{ID: "ship-task", Status: "merged", Repository: root, Branch: "ship-task"}

	plan := service.Plan(context.Background(), task)
	if !plan.Eligible {
		t.Fatalf("plan blocked: %v", plan.Blockers)
	}
	if len(plan.Sessions) != 1 {
		t.Fatalf("unexpected plan: %#v", plan)
	}
	result, err := service.Execute(context.Background(), plan, true)
	if err != nil {
		t.Fatal(err)
	}
	if !result.WorktreeRemoved || !result.BranchDeleted || result.SessionsKilled != 1 {
		t.Fatalf("unexpected result: %#v", result)
	}
	if _, err := os.Stat(worktree); !os.IsNotExist(err) {
		t.Fatalf("worktree still exists: %v", err)
	}
	if output, err := exec.Command("git", "-C", root, "show-ref", "--verify", "refs/heads/ship-task").CombinedOutput(); err == nil {
		t.Fatalf("branch still exists: %s", output)
	}
	killed, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(killed), "$9") {
		t.Fatalf("session was not killed: %s", killed)
	}
}

func TestCleanupKillsOnlyMatchingWorkstreamTaskWindow(t *testing.T) {
	t.Setenv("TMUX_PANE", "")
	root, worktree := cleanupRepository(t)
	log := filepath.Join(t.TempDir(), "tmux.log")
	tmux := filepath.Join(t.TempDir(), "tmux")
	script := `#!/bin/sh
if [ "$1" = list-panes ]; then
  printf '%s\n' '$9\037tasks-core\037ship-task\037` + worktree + `\037` + root + `\037ws-test\037workstream\037@1\037ship-task\037task'
  printf '%s\n' '$9\037tasks-core\037sibling\037/tmp/sibling\037` + root + `\037ws-test\037workstream\037@2\037sibling\037task'
  exit 0
fi
if [ "$1" = kill-window ] || [ "$1" = kill-session ]; then
  printf '%s %s\n' "$1" "$3" >> '` + log + `'
  exit 0
fi
exit 1
`
	if err := os.WriteFile(tmux, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	service := &Service{Git: "git", Tmux: tmux}
	plan := service.Plan(context.Background(), workload.Entity{ID: "ship-task", Status: "merged", Repository: root, Branch: "ship-task"})
	if !plan.Eligible || len(plan.Sessions) != 1 || plan.Sessions[0].WindowID != "@1" {
		t.Fatalf("plan = %#v", plan)
	}
	result, err := service.Execute(context.Background(), plan, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.WindowsKilled != 1 || result.SessionsKilled != 0 {
		t.Fatalf("result = %#v", result)
	}
	content, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(content)) != "kill-window @1" {
		t.Fatalf("cleanup command = %q", content)
	}
}

func TestCleanupLastTaskWindowPreservesWorkstreamPlanner(t *testing.T) {
	t.Setenv("TMUX_PANE", "")
	root, worktree := cleanupRepository(t)
	log := filepath.Join(t.TempDir(), "tmux.log")
	tmux := filepath.Join(t.TempDir(), "tmux")
	script := `#!/bin/sh
if [ "$1" = list-panes ]; then
  printf '%s\n' '$9\037tasks-core\037\037` + root + `\037` + root + `\037ws-test\037workstream\037@plan\037plan-core\037'
  printf '%s\n' '$9\037tasks-core\037ship-task\037` + worktree + `\037` + root + `\037ws-test\037workstream\037@task\037ship-task\037task'
  exit 0
fi
if [ "$1" = kill-window ] || [ "$1" = kill-session ]; then
  printf '%s %s\n' "$1" "$3" >> '` + log + `'
  exit 0
fi
exit 1
`
	if err := os.WriteFile(tmux, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	service := &Service{Git: "git", Tmux: tmux}
	plan := service.Plan(context.Background(), workload.Entity{ID: "ship-task", Status: "merged", Repository: root, Branch: "ship-task"})
	if !plan.Eligible || len(plan.Sessions) != 1 || plan.Sessions[0].WindowID != "@task" {
		t.Fatalf("plan = %#v", plan)
	}
	result, err := service.Execute(context.Background(), plan, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.WindowsKilled != 1 || result.SessionsKilled != 0 {
		t.Fatalf("result = %#v", result)
	}
	content, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(content)) != "kill-window @task" {
		t.Fatalf("cleanup removed planner session: %q", content)
	}
}

func TestTrustMergedOverridesOnlyMergeVerificationWithSyncedMain(t *testing.T) {
	t.Setenv("TMUX_PANE", "")
	root, worktree := cleanupRepository(t)
	if err := os.WriteFile(filepath.Join(worktree, "unverified.txt"), []byte("unverified\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, worktree, "add", "unverified.txt")
	runGit(t, worktree, "commit", "-qm", "conflict-resolved elsewhere")
	service := &Service{Git: "git", Tmux: fakeTmux(t, filepath.Join(t.TempDir(), "tmux.log"), worktree, "ship-task")}
	task := workload.Entity{ID: "ship-task", Status: "merged", Repository: root, Branch: "ship-task"}
	if plan := service.Plan(context.Background(), task); plan.Eligible {
		t.Fatal("unverified branch unexpectedly eligible without override")
	}
	plan := service.PlanWithOptions(context.Background(), task, PlanOptions{TrustMerged: true})
	if !plan.Eligible || !plan.TrustedMerged {
		t.Fatalf("trusted merged plan blocked: %#v", plan)
	}
}

func TestPlanBlocksUserChangesAndActiveTask(t *testing.T) {
	t.Setenv("TMUX_PANE", "")
	root, worktree := cleanupRepository(t)
	if err := os.WriteFile(filepath.Join(worktree, "user-change.txt"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	service := &Service{Git: "git", Tmux: fakeTmux(t, filepath.Join(t.TempDir(), "tmux.log"), worktree, "ship-task")}
	plan := service.Plan(context.Background(), workload.Entity{ID: "ship-task", Status: "active", Repository: root, Branch: "ship-task"})
	if plan.Eligible {
		t.Fatal("active dirty task unexpectedly eligible")
	}
	joined := strings.Join(plan.Blockers, "; ")
	if !strings.Contains(joined, "not merged") || !strings.Contains(joined, "user changes") {
		t.Fatalf("unexpected blockers: %v", plan.Blockers)
	}
}

func TestPlanBlocksTrackedSettingsSymlink(t *testing.T) {
	t.Setenv("TMUX_PANE", "")
	root, worktree := cleanupRepository(t)
	settings := filepath.Join(worktree, ".claude", "settings.local.json")
	if err := os.Remove(settings); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, ".claude", "settings.local.json"), settings); err != nil {
		t.Fatal(err)
	}
	service := &Service{Git: "git", Tmux: fakeTmux(t, filepath.Join(t.TempDir(), "tmux.log"), worktree, "ship-task")}
	plan := service.Plan(context.Background(), workload.Entity{ID: "ship-task", Status: "merged", Repository: root, Branch: "ship-task"})
	if plan.Eligible {
		t.Fatal("tracked settings type change unexpectedly eligible for cleanup")
	}
	if joined := strings.Join(plan.Blockers, "; "); !strings.Contains(joined, "user changes") || !strings.Contains(joined, ".claude/settings.local.json") {
		t.Fatalf("settings change not reported: %v", plan.Blockers)
	}
}

func cleanupRepository(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	runGit(t, root, "init", "-q", "-b", "main")
	runGit(t, root, "config", "user.email", "test@example.com")
	runGit(t, root, "config", "user.name", "Test")
	if err := os.MkdirAll(filepath.Join(root, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".claude", "settings.local.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", ".")
	runGit(t, root, "commit", "-qm", "base")
	runGit(t, root, "branch", "ship-task")
	worktree := filepath.Join(t.TempDir(), "ship-task")
	runGit(t, root, "worktree", "add", "-q", worktree, "ship-task")
	if err := os.WriteFile(filepath.Join(worktree, "task.txt"), []byte("task\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, worktree, "add", "task.txt")
	runGit(t, worktree, "commit", "-qm", "task")
	if err := os.WriteFile(filepath.Join(root, "main.txt"), []byte("main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "main.txt")
	runGit(t, root, "commit", "-qm", "main")
	runGit(t, root, "cherry-pick", "ship-task")
	runGit(t, root, "update-ref", "refs/remotes/origin/main", "HEAD")
	return root, worktree
}

func fakeTmux(t *testing.T, log, worktree, taskID string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tmux")
	script := `#!/bin/sh
if [ "$1" = list-panes ]; then
  printf '%s\n' '$9\037task-session\037` + taskID + `\037` + worktree + `\037/repo\037ws-test\037task\037@9\037fx\037'
  exit 0
fi
if [ "$1" = kill-session ]; then
  printf '%s\n' "$3" >> '` + log + `'
  exit 0
fi
exit 1
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func runGit(t *testing.T, directory string, args ...string) {
	t.Helper()
	arguments := append([]string{"-c", "commit.gpgsign=false", "-C", directory}, args...)
	if output, err := exec.Command("git", arguments...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}
