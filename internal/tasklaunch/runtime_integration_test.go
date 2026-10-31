package tasklaunch

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jarneson/fx-workbench/internal/workload"
)

func TestRuntimeLaunchesTwoTasksInOneWorkstreamSession(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires unix shell and tmux")
	}
	tmux, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux not installed")
	}
	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	repository := filepath.Join(root, "repository")
	runTestCommand(t, "", "git", "init", "--bare", "--initial-branch=main", origin)
	runTestCommand(t, "", "git", "clone", origin, repository)
	runTestCommand(t, repository, "git", "config", "user.email", "test@example.com")
	runTestCommand(t, repository, "git", "config", "user.name", "Test")
	runTestCommand(t, repository, "git", "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(repository, "README.md"), []byte("test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestCommand(t, repository, "git", "add", "README.md")
	runTestCommand(t, repository, "git", "commit", "-m", "initial")
	runTestCommand(t, repository, "git", "push", "-u", "origin", "main")
	for _, branch := range []string{"integration-one", "integration-two"} {
		runTestCommand(t, repository, "git", "branch", branch)
		runTestCommand(t, repository, "git", "push", "origin", branch)
		runTestCommand(t, repository, "git", "branch", "-D", branch)
		runTestCommand(t, repository, "git", "update-ref", "-d", "refs/remotes/origin/"+branch)
	}

	socket := "fx-workbench-test-" + strings.ReplaceAll(filepath.Base(root), ".", "-")
	wrapper := filepath.Join(root, "tmux-test")
	fx := filepath.Join(root, "fx-test")
	if err := os.WriteFile(wrapper, []byte("#!/bin/sh\nexec "+tmux+" -L "+socket+" \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fx, []byte("#!/bin/sh\n"+wrapper+" set-option -p -t \"$TMUX_PANE\" @fx_session_id task-session-$TMUX_PANE\n"+wrapper+" set-option -p -t \"$TMUX_PANE\" @fx_state idle\nsleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = exec.Command(wrapper, "kill-server").Run() })

	runtimeAdapter := NewRuntime()
	runtimeAdapter.TmuxBinary = wrapper
	runtimeAdapter.WorktreeRoot = filepath.Join(root, "worktrees")
	runtimeAdapter.InstallCommand = "true"
	runtimeAdapter.FXCommand = fx
	runtimeAdapter.NoSwitch = true
	runtimeAdapter.ReadyTimeout = 5 * time.Second
	service := NewService(runtimeAdapter)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	var results []Result
	for index, branch := range []string{"integration-one", "integration-two"} {
		packet := filepath.Join(root, branch+".md")
		if err := os.WriteFile(packet, []byte("# Integration task\n\nDo the thing.\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		result, err := service.Launch(ctx, workload.Entity{
			Type: "task", ID: branch, Title: "Integration task", Project: "demo", Workstream: "core",
			Repository: repository, Branch: branch, SourcePath: packet,
		}, "", nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(result.Worktree, ".git")); err != nil {
			t.Fatalf("worktree not created: %v", err)
		}
		if index == 0 && !result.CreatedSession || index == 1 && result.CreatedSession {
			t.Fatalf("created session for launch %d = %v", index, result.CreatedSession)
		}
		results = append(results, result)
	}
	if results[0].Session != results[1].Session || results[0].FXWindow == results[1].FXWindow {
		t.Fatalf("launch identities = %#v", results)
	}
	windows := strings.Fields(runTestCommand(t, "", wrapper, "list-windows", "-t", results[0].Session, "-F", "#{window_id}"))
	if len(windows) != 2 {
		t.Fatalf("windows = %v, want two agent-only task windows", windows)
	}
	for index, result := range results {
		taskID := strings.TrimSpace(runTestCommand(t, "", wrapper, "show-options", "-w", "-v", "-t", result.FXWindow, "@fx_task_id"))
		if taskID != []string{"integration-one", "integration-two"}[index] {
			t.Fatalf("window task tag = %q", taskID)
		}
		if result.FXSessionID == "" {
			t.Fatal("fx session id was not captured")
		}
		profileID := strings.TrimSpace(runTestCommand(t, "", wrapper, "show-options", "-w", "-v", "-t", result.FXWindow, "@fx_agent_profile"))
		profileDigest := strings.TrimSpace(runTestCommand(t, "", wrapper, "show-options", "-w", "-v", "-t", result.FXWindow, "@fx_agent_profile_digest"))
		if profileID != "coding" || !strings.HasPrefix(profileDigest, "sha256:") {
			t.Fatalf("profile tags = %q %q", profileID, profileDigest)
		}
	}
}

func runTestCommand(t *testing.T, directory, name string, args ...string) string {
	t.Helper()
	command := exec.Command(name, args...)
	command.Dir = directory
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, output)
	}
	return string(output)
}
