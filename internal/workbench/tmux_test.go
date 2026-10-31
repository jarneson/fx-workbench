package workbench

import (
	"strings"
	"testing"
	"time"
)

func TestParseSnapshotAggregatesAndPrioritizesSessions(t *testing.T) {
	row := func(fields ...string) string { return strings.Join(fields, outputSeparator) }
	input := strings.Join([]string{
		row("$1", "quiet", "1", "1700000000", "root", "", "ws-one", "/tmp/repo", "/tmp/repo", "", "demo", "", "@1", "0", "shell", "1", "", "", "/tmp/repo", "/tmp/repo", "", "", "%1", "0", "1", "zsh", "/tmp/quiet", "", "", "", ""),
		row("$2", "busy", "0", "1700000001", "task", "$1", "ws-one", "/tmp/repo", "/tmp/busy", "task-one", "demo", "core", "@2", "0", "fx", "1", "", "task-one", "/tmp/repo", "/tmp/busy", "task-one", "fx-one", "%2", "0", "1", "fx", "/tmp/busy", "running", "Implement parser", "", "fx-one"),
		row("$3", "blocked", "0", "1700000002", "task", "$1", "ws-one", "/tmp/repo", "/tmp/blocked", "task-two", "demo", "core", "@3", "0", "fx", "1", "", "task-two", "/tmp/repo", "/tmp/blocked", "task-two", "fx-two", "%3", "0", "1", "fx", "/tmp/blocked", "idle", "Need decision", "1", "fx-two"),
	}, "\n")

	snapshot := parseSnapshot([]byte(input), time.Unix(1700000100, 0))
	if got := len(snapshot.Sessions); got != 3 {
		t.Fatalf("got %d sessions, want 3", got)
	}
	if got := snapshot.Sessions[0].Name; got != "blocked" {
		t.Fatalf("first session = %q, want blocked", got)
	}
	if got := snapshot.Sessions[0].State; got != StateAttention {
		t.Fatalf("blocked state = %q, want %q", got, StateAttention)
	}
	if got := snapshot.Sessions[1].State; got != StateRunning {
		t.Fatalf("busy state = %q, want %q", got, StateRunning)
	}
	if got := snapshot.Sessions[1].Parent; got != "$1" {
		t.Fatalf("busy parent = %q, want $1", got)
	}
	if got := snapshot.Sessions[1].ParentName; got != "quiet" {
		t.Fatalf("busy parent name = %q, want quiet", got)
	}
	if got := snapshot.Sessions[1].TaskID; got != "task-one" {
		t.Fatalf("busy task = %q, want task-one", got)
	}
	if pane := snapshot.Sessions[1].Windows[0].Panes[0]; pane.TaskID != "task-one" || pane.FXSessionID != "fx-one" {
		t.Fatalf("busy pane identity = %#v", pane)
	}
	if got := snapshot.Sessions[2].Agents; got != 0 {
		t.Fatalf("quiet agents = %d, want 0", got)
	}
}

func TestParseSnapshotInfersLegacyWorktreeIdentity(t *testing.T) {
	row := strings.Join([]string{
		"$1", "legacy", "0", "1700000000", "", "", "", "", "", "", "", "",
		"@1", "0", "shell", "1", "", "", "", "", "", "",
		"%1", "0", "1", "zsh", "/Users/example/dev/worktrees/widgets/fix-parser", "", "", "", "",
	}, outputSeparator)
	snapshot := parseSnapshot([]byte(row), time.Now())
	if got := len(snapshot.Sessions); got != 1 {
		t.Fatalf("got %d sessions, want 1", got)
	}
	session := snapshot.Sessions[0]
	if session.Explicit || session.Role != "worktree" || session.Repository != "widgets" {
		t.Fatalf("legacy identity = %#v", session)
	}
}

func TestParseSnapshotRetainsDistinctTaskWindows(t *testing.T) {
	row := func(fields ...string) string { return strings.Join(fields, outputSeparator) }
	base := []string{"$1", "tasks-core", "0", "1700000000", "workstream", "", "ws-one", "/tmp/repo", "", "", "demo", "core"}
	worker := func(window, task, worktree, fxSession, pane string) string {
		return row(append(append([]string{}, base...), window, "0", task, "1", "task", task, "/tmp/repo", worktree, task, fxSession, pane, "0", "1", "fx", worktree, "idle", task, "", fxSession)...)
	}
	snapshot := parseSnapshot([]byte(worker("@1", "task-one", "/tmp/one", "fx-one", "%1")+"\n"+worker("@2", "task-two", "/tmp/two", "fx-two", "%2")), time.Now())
	if len(snapshot.Sessions) != 1 || len(snapshot.Sessions[0].Windows) != 2 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	session := snapshot.Sessions[0]
	if session.TaskID != "" || session.Worktree != "" || session.Project != "demo" || session.Workstream != "core" {
		t.Fatalf("session identity = %#v", session)
	}
	if session.Windows[0].TaskID != "task-one" || session.Windows[1].TaskID != "task-two" || session.Windows[1].FXSessionID != "fx-two" {
		t.Fatalf("windows = %#v", session.Windows)
	}
}

func TestParseSnapshotDoesNotClassifyPlannerAsTaskWindow(t *testing.T) {
	fields := []string{
		"$1", "tasks-core", "0", "1700000000", "workstream", "", "ws-one", "/tmp/repo", "", "", "demo", "core",
		"@plan", "0", "plan-core", "1", "", "", "/tmp/repo", "", "", "",
		"%plan", "0", "1", "fx", "/tmp/repo", "idle", "planner", "", "planner-session",
	}
	snapshot := parseSnapshot([]byte(strings.Join(fields, outputSeparator)), time.Now())
	if len(snapshot.Sessions) != 1 || len(snapshot.Sessions[0].Windows) != 1 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	window := snapshot.Sessions[0].Windows[0]
	if window.Role != "" || window.TaskID != "" || window.Worktree != "" {
		t.Fatalf("planner was classified as task window: %#v", window)
	}
}

func TestParseSnapshotSkipsMalformedRows(t *testing.T) {
	snapshot := parseSnapshot([]byte("not-enough-fields\n"), time.Now())
	if len(snapshot.Sessions) != 0 {
		t.Fatalf("got %d sessions, want 0", len(snapshot.Sessions))
	}
}

func TestNewTmuxSourceIgnoresUnexpandedClientFormat(t *testing.T) {
	t.Setenv("FX_WORKBENCH_CLIENT", "#{client_tty}")
	source := NewTmuxSource("tmux")
	if source.client != "" {
		t.Fatalf("client = %q, want empty", source.client)
	}
}

func TestFocusTargetPrefersAttentionThenRunningAgentPane(t *testing.T) {
	session := Session{Windows: []Window{
		{ID: "@1", Panes: []Pane{{ID: "%1", Active: true, AgentState: StateIdle, AgentTitle: "idle"}}},
		{ID: "@2", Panes: []Pane{{ID: "%2", AgentState: StateRunning, AgentTitle: "running"}}},
		{ID: "@3", Panes: []Pane{{ID: "%3", AgentState: StateIdle, AgentTitle: "blocked", NeedsAttention: true}}},
	}}
	window, pane := focusTarget(session)
	if window != "@3" || pane != "%3" {
		t.Fatalf("target = %s/%s, want @3/%%3", window, pane)
	}
	session.Windows[2].Panes[0].NeedsAttention = false
	window, pane = focusTarget(session)
	if window != "@2" || pane != "%2" {
		t.Fatalf("target = %s/%s, want @2/%%2", window, pane)
	}
}

func BenchmarkModelView(b *testing.B) {
	sessions := make([]Session, 20)
	for i := range sessions {
		sessions[i] = Session{
			Name:      "workspace-with-a-reasonably-long-name",
			Path:      "/Users/example/dev/worktrees/project/feature",
			CreatedAt: time.Now().Add(-time.Hour),
			State:     StateRunning,
			Agents:    1,
			Windows: []Window{{
				Index: "1", Name: "fx: implementing a task", Active: true,
				Panes: []Pane{{AgentState: StateRunning, AgentTitle: "Implement the workbench renderer"}},
			}},
		}
	}
	model := Model{snapshot: Snapshot{Sessions: sessions}, width: 140, height: 45}
	b.ResetTimer()
	for range b.N {
		_ = model.View()
	}
}
