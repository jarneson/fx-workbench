package mainsync

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type fakeRunner struct {
	calls  []string
	dirty  bool
	failAt string
}

func (runner *fakeRunner) Run(_ context.Context, directory string, args ...string) ([]byte, error) {
	call := directory + " git " + strings.Join(args, " ")
	runner.calls = append(runner.calls, call)
	if strings.Join(args, " ") == runner.failAt {
		return []byte("command failed"), errors.New("exit 1")
	}
	switch strings.Join(args, " ") {
	case "rev-parse --show-toplevel":
		return []byte("/repo\n"), nil
	case "worktree list --porcelain":
		return []byte("worktree /trees/task\nbranch refs/heads/task\n\nworktree /repo-main\nbranch refs/heads/main\n"), nil
	case "status --porcelain":
		if runner.dirty {
			return []byte(" M owned.txt\n"), nil
		}
	case "rev-parse main", "rev-parse origin/main":
		return []byte("abc123\n"), nil
	}
	return nil, nil
}

func TestSyncOrdersFetchCleanCheckFastForwardAndEquality(t *testing.T) {
	runner := &fakeRunner{}
	service := &Service{runner: runner}
	result, err := service.Sync(context.Background(), "/repo-feature")
	if err != nil {
		t.Fatal(err)
	}
	if result.MainWorktree != "/repo-main" {
		t.Fatalf("main worktree = %q", result.MainWorktree)
	}
	want := []string{
		"/repo-feature git rev-parse --show-toplevel",
		"/repo git worktree list --porcelain",
		"/repo-main git fetch origin main",
		"/repo-main git status --porcelain",
		"/repo-main git merge --ff-only origin/main",
		"/repo-main git rev-parse main",
		"/repo-main git rev-parse origin/main",
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("calls = %#v, want %#v", runner.calls, want)
	}
}

func TestSyncStopsBeforeFastForwardWhenMainIsDirty(t *testing.T) {
	runner := &fakeRunner{dirty: true}
	_, err := (&Service{runner: runner}).Sync(context.Background(), "/repo")
	if err == nil || !strings.Contains(err.Error(), "main worktree is dirty: /repo-main") {
		t.Fatalf("error = %v", err)
	}
	for _, call := range runner.calls {
		if strings.Contains(call, " merge ") {
			t.Fatalf("dirty main was modified: %v", runner.calls)
		}
	}
}

func TestSyncReportsFetchRecovery(t *testing.T) {
	runner := &fakeRunner{failAt: "fetch origin main"}
	_, err := (&Service{runner: runner}).Sync(context.Background(), "/repo")
	if err == nil || !strings.Contains(err.Error(), "verify the origin remote and network access") {
		t.Fatalf("error = %v", err)
	}
}

func TestSyncReportsDivergentMainRecovery(t *testing.T) {
	runner := &fakeRunner{failAt: "merge --ff-only origin/main"}
	_, err := (&Service{runner: runner}).Sync(context.Background(), "/repo")
	if err == nil || !strings.Contains(err.Error(), "reconcile the divergent history yourself") {
		t.Fatalf("error = %v", err)
	}
}

func TestSyncAdvancesOnlyMainWorktree(t *testing.T) {
	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	main := filepath.Join(root, "main")
	writer := filepath.Join(root, "writer")
	task := filepath.Join(root, "task")
	runGit(t, "", "init", "--bare", "--initial-branch=main", origin)
	runGit(t, "", "clone", origin, main)
	configureGit(t, main)
	if err := os.WriteFile(filepath.Join(main, "value.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, main, "add", "value.txt")
	runGit(t, main, "commit", "-m", "initial")
	runGit(t, main, "push", "-u", "origin", "main")
	runGit(t, main, "worktree", "add", "-b", "task", task, "main")
	taskBefore := strings.TrimSpace(runGit(t, task, "rev-parse", "HEAD"))

	runGit(t, "", "clone", origin, writer)
	configureGit(t, writer)
	if err := os.WriteFile(filepath.Join(writer, "value.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, writer, "add", "value.txt")
	runGit(t, writer, "commit", "-m", "advance")
	runGit(t, writer, "push", "origin", "main")
	remote := strings.TrimSpace(runGit(t, writer, "rev-parse", "HEAD"))

	result, err := New().Sync(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	canonicalMain, err := filepath.EvalSymlinks(main)
	if err != nil {
		t.Fatal(err)
	}
	if result.MainWorktree != canonicalMain {
		t.Fatalf("main worktree = %q, want %q", result.MainWorktree, canonicalMain)
	}
	if got := strings.TrimSpace(runGit(t, main, "rev-parse", "HEAD")); got != remote {
		t.Fatalf("main = %s, want %s", got, remote)
	}
	if got := strings.TrimSpace(runGit(t, task, "rev-parse", "HEAD")); got != taskBefore {
		t.Fatalf("task worktree moved from %s to %s", taskBefore, got)
	}
}

func configureGit(t *testing.T, directory string) {
	t.Helper()
	runGit(t, directory, "config", "user.email", "test@example.com")
	runGit(t, directory, "config", "user.name", "Test")
	runGit(t, directory, "config", "commit.gpgsign", "false")
}

func runGit(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = directory
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return string(output)
}
