package planner

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jarneson/fx-workbench/internal/tasklaunch"
	runtimeworkbench "github.com/jarneson/fx-workbench/internal/workbench"
	"github.com/jarneson/fx-workbench/internal/workload"
)

func TestTmuxPlannerAndTaskShareWorkstreamSession(t *testing.T) {
	tmux, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux is not installed")
	}
	directory := t.TempDir()
	socket := fmt.Sprintf("fx-workstream-test-%d-%d", os.Getpid(), time.Now().UnixNano())
	wrapper := filepath.Join(directory, "tmux")
	if err := os.WriteFile(wrapper, []byte(fmt.Sprintf("#!/bin/sh\nexec %q -L %q -f /dev/null \"$@\"\n", tmux, socket)), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = exec.Command(wrapper, "kill-server").Run() })

	promptLog := filepath.Join(directory, "prompt.log")
	agent := filepath.Join(directory, "fx-tmux-test")
	agentScript := fmt.Sprintf("#!/bin/sh\ntmux set-option -p -t \"$TMUX_PANE\" @fx_state idle\ntmux set-option -p -t \"$TMUX_PANE\" @fx_session_id session-$TMUX_PANE\nwhile IFS= read -r line; do printf '%%s\\n' \"$line\" >> %q; done\n", promptLog)
	if err := os.WriteFile(agent, []byte(agentScript), 0o755); err != nil {
		t.Fatal(err)
	}
	sidebar := filepath.Join(directory, "sidebar-test")
	if err := os.WriteFile(sidebar, []byte("#!/bin/sh\nwhile :; do sleep 1; done\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	root := filepath.Join(directory, "widgets")
	worktree := filepath.Join(directory, "task-worktree")
	for _, path := range []string{root, worktree} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	identity, err := runtimeworkbench.NewWorkstreamIdentity("demo", "core", root)
	if err != nil {
		t.Fatal(err)
	}
	plan := Plan{
		Project:        workloadProject("demo", filepath.Join(directory, "project.md")),
		Workstream:     workload.Entity{ID: "core", Project: "demo"},
		RootRepository: root, WorkspaceID: identity.WorkspaceID, SessionName: identity.SessionName,
		WindowName: "plan-core", Prompt: "integration planner prompt",
	}
	plannerRuntime := NewTmuxRuntime(wrapper, agent)
	plannerRuntime.SidebarCommand = sidebar
	plannerRuntime.InsideTmux = false
	ctx := context.Background()
	first, err := plannerRuntime.Ensure(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	if !first.CreatedSession || !first.CreatedPlanner || first.SessionName != identity.SessionName {
		t.Fatalf("planner first launch = %#v", first)
	}
	waitForFile(t, promptLog, "integration planner prompt")

	taskRuntime := &tasklaunch.Runtime{
		TmuxBinary: wrapper, FXCommand: agent, Runner: tasklaunch.ExecRunner{}, Environment: os.Environ(),
	}
	worker, err := taskRuntime.Launch(ctx, tasklaunch.Target{
		Repository: root, Worktree: worktree, Session: identity.SessionName, Window: "ship-task",
		Project: "demo", Workstream: "core", TaskID: "ship-task", Branch: "ship-task",
	})
	if err != nil {
		t.Fatal(err)
	}
	if worker.CreatedSession || !worker.CreatedWindow {
		t.Fatalf("task launch = %#v", worker)
	}

	second, err := plannerRuntime.Ensure(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	if second.CreatedSession || second.CreatedPlanner || second.SessionID != first.SessionID || second.PaneID != first.PaneID {
		t.Fatalf("planner reuse = %#v, first = %#v", second, first)
	}
	contents, err := os.ReadFile(promptLog)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(contents), "integration planner prompt") != 1 {
		t.Fatalf("prompt delivered more than once: %q", contents)
	}

	format := "#{window_index}|#{window_id}|#{window_name}|#{@fx_planner}|#{@fx_task_role}|#{@fx_task_id}"
	output, err := exec.Command(wrapper, "list-windows", "-t", identity.SessionName, "-F", format).CombinedOutput()
	if err != nil {
		t.Fatalf("inspect shared session: %s", output)
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], "|"+first.WindowID+"|plan-core|1||") || !strings.Contains(lines[1], "|"+worker.Window+"|ship-task||task|ship-task") {
		t.Fatalf("shared window order/identity = %q", output)
	}
	sessionFormat := "#{@fx_workflow_role}|#{@fx_project_id}|#{@fx_workstream_id}|#{@fx_repository}|#{renumber-windows}"
	output, err = exec.Command(wrapper, "display-message", "-p", "-t", identity.SessionName, sessionFormat).CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(output)); got != "workstream|demo|core|"+identity.Repository+"|1" {
		t.Fatalf("shared session identity = %q", got)
	}
}

func waitForFile(t *testing.T, path, text string) {
	t.Helper()
	for range 100 {
		contents, _ := os.ReadFile(path)
		if strings.Contains(string(contents), text) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s did not contain %q", path, text)
}
