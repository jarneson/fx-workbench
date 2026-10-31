package workbench

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jarneson/fx-workbench/internal/mainsync"
)

type testMainSync struct {
	calls []string
	err   error
}

func (syncer *testMainSync) Sync(_ context.Context, repository string) (mainsync.Result, error) {
	syncer.calls = append(syncer.calls, repository)
	return mainsync.Result{MainWorktree: repository}, syncer.err
}

func TestEnsureRootSynchronizesBeforeInspectingTmux(t *testing.T) {
	repository := t.TempDir()
	syncer := &testMainSync{err: errors.New("dirty main")}
	source := NewTmuxSource("/does/not/exist")
	source.mainSync = syncer
	_, err := source.EnsureRoot(context.Background(), repository)
	if err == nil || !strings.Contains(err.Error(), "dirty main") {
		t.Fatalf("error = %v", err)
	}
	if len(syncer.calls) != 1 {
		t.Fatalf("sync calls = %v", syncer.calls)
	}
}

func TestTmuxOpenWorkspaceCreatesReusesAndFocusesOriginatingClient(t *testing.T) {
	tmux, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux is not installed")
	}
	directory := t.TempDir()
	socket := fmt.Sprintf("fx-workbench-open-test-%d", os.Getpid())
	wrapper := filepath.Join(directory, "tmux")
	contents := fmt.Sprintf("#!/bin/sh\nexec %q -L %q -f /dev/null \"$@\"\n", tmux, socket)
	if err := os.WriteFile(wrapper, []byte(contents), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = exec.Command(wrapper, "kill-server").Run() })

	repository := filepath.Join(directory, "repository")
	worktree := filepath.Join(directory, "worktree with spaces")
	for _, path := range []string{repository, worktree} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if output, err := exec.Command(wrapper, "new-session", "-d", "-s", "origin", "-c", repository).CombinedOutput(); err != nil {
		t.Fatalf("create origin: %s", commandError(output, err))
	}

	clients := make([]*exec.Cmd, 0, 2)
	inputs := make([]io.WriteCloser, 0, 2)
	for range 2 {
		command := exec.Command(wrapper, "-C", "attach-session", "-t", "origin")
		input, err := command.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		command.Stdout, command.Stderr = io.Discard, io.Discard
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		clients, inputs = append(clients, command), append(inputs, input)
	}
	t.Cleanup(func() {
		for index := range clients {
			_ = inputs[index].Close()
			_ = clients[index].Process.Kill()
			_, _ = clients[index].Process.Wait()
		}
	})

	var clientNames []string
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		output, _ := exec.Command(wrapper, "list-clients", "-F", "#{client_name}").Output()
		clientNames = strings.Fields(string(output))
		if len(clientNames) == 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(clientNames) != 2 {
		t.Fatalf("clients = %#v, want two", clientNames)
	}

	source := NewTmuxSource(wrapper)
	source.mainSync = &testMainSync{}
	source.client = clientNames[0]
	repositoryOpen, err := source.OpenWorkspace(context.Background(), Workspace{Kind: WorkspaceRepository, Label: "repository", Path: repository, Repository: repository})
	if err != nil {
		t.Fatal(err)
	}
	if repositoryOpen.Created || repositoryOpen.Session.ID == "" || repositoryOpen.Session.Role != RoleRoot {
		t.Fatalf("legacy repository session was not adopted as root: %#v", repositoryOpen)
	}
	windowName, err := exec.Command(wrapper, "display-message", "-p", "-t", repositoryOpen.Session.ID+":", "#{window_name}").Output()
	if err != nil || strings.TrimSpace(string(windowName)) != filepath.Base(repository) {
		t.Fatalf("repository terminal name = %q, err=%v", strings.TrimSpace(string(windowName)), err)
	}

	workspace := Workspace{Kind: WorkspaceWorktree, Label: "repository/worktree", Path: worktree, Repository: repository}
	first, err := source.OpenWorkspace(context.Background(), workspace)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Created {
		t.Fatal("first open did not create a session")
	}
	second, err := source.OpenWorkspace(context.Background(), workspace)
	if err != nil {
		t.Fatal(err)
	}
	if second.Created || second.Session.ID != first.Session.ID {
		t.Fatalf("workspace was not reused: first=%#v second=%#v", first, second)
	}

	output, err := exec.Command(wrapper, "list-clients", "-F", "#{client_name}|#{client_session}").CombinedOutput()
	if err != nil {
		t.Fatalf("list clients: %s", commandError(output, err))
	}
	sessions := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		fields := strings.SplitN(line, "|", 2)
		if len(fields) == 2 {
			sessions[fields[0]] = fields[1]
		}
	}
	if sessions[clientNames[0]] != first.Session.Name {
		t.Fatalf("originating client session = %q, want %q", sessions[clientNames[0]], first.Session.Name)
	}
	if sessions[clientNames[1]] != "origin" {
		t.Fatalf("other client moved to %q, want origin", sessions[clientNames[1]])
	}
	output, err = exec.Command(wrapper, "list-sessions", "-F", "#{session_name}").CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(strings.TrimSpace(string(output)), "\n") + 1; got != 2 {
		t.Fatalf("session count = %d, want origin plus one workspace: %s", got, output)
	}
}

func TestTmuxWorkspaceIdentityIntegration(t *testing.T) {
	tmux, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux is not installed")
	}
	directory := t.TempDir()
	socket := fmt.Sprintf("fx-workbench-test-%d", os.Getpid())
	wrapper := filepath.Join(directory, "tmux")
	contents := fmt.Sprintf("#!/bin/sh\nexec %q -L %q -f /dev/null \"$@\"\n", tmux, socket)
	if err := os.WriteFile(wrapper, []byte(contents), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = exec.Command(wrapper, "kill-server").Run() })

	repository := filepath.Join(directory, "one", "widgets")
	collisionRepository := filepath.Join(directory, "two", "widgets")
	worktree := filepath.Join(directory, "worktrees", "widgets", "same-branch")
	collisionWorktree := filepath.Join(directory, "other-worktrees", "widgets", "same-branch")
	for _, path := range []string{repository, collisionRepository, worktree, collisionWorktree} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	source := NewTmuxSource(wrapper)
	source.mainSync = &testMainSync{}
	ctx := context.Background()
	root, err := source.EnsureRoot(ctx, repository)
	if err != nil {
		t.Fatal(err)
	}
	if !root.Created {
		t.Fatal("first root bootstrap did not create a session")
	}
	if root.Name != filepath.Base(repository) || root.WindowID == "" || root.PaneID == "" {
		t.Fatalf("root repository layout = %#v", root)
	}
	reused, err := source.EnsureRoot(ctx, repository)
	if err != nil {
		t.Fatal(err)
	}
	if reused.Created || reused.ID != root.ID {
		t.Fatalf("root was not reused: first=%#v second=%#v", root, reused)
	}
	collisionRoot, err := source.EnsureRoot(ctx, collisionRepository)
	if err != nil {
		t.Fatal(err)
	}
	if collisionRoot.Name == root.Name {
		t.Fatalf("same-basename repositories collided at %q", root.Name)
	}

	task, _ := NewIdentity(RoleTask, repository, worktree, "task-one")
	collisionTask, _ := NewIdentity(RoleTask, repository, collisionWorktree, "task-two")
	if SessionName(task) == SessionName(collisionTask) {
		t.Fatalf("same-basename worktrees collided at %q", SessionName(task))
	}
	if output, err := exec.Command(wrapper, "new-session", "-d", "-s", SessionName(task), "-c", worktree).CombinedOutput(); err != nil {
		t.Fatalf("create task session: %s", commandError(output, err))
	}
	if err := source.TagSession(ctx, SessionName(task), task); err != nil {
		t.Fatal(err)
	}
	format := "#{@fx_workflow_role}|#{@fx_workspace_id}|#{@fx_parent_session}|#{@fx_repository}|#{@fx_worktree}|#{@fx_task_id}"
	output, err := exec.Command(wrapper, "display-message", "-p", "-t", SessionName(task), format).CombinedOutput()
	if err != nil {
		t.Fatalf("read task metadata: %s", commandError(output, err))
	}
	fields := strings.Split(strings.TrimSpace(string(output)), "|")
	want := []string{RoleTask, root.Identity.WorkspaceID, root.ID, root.Identity.Repository, task.Worktree, "task-one"}
	if strings.Join(fields, "|") != strings.Join(want, "|") {
		t.Fatalf("task metadata = %#v, want %#v", fields, want)
	}
}
