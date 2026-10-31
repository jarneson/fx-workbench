package taskui

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	runtimeworkbench "github.com/jarneson/fx-workbench/internal/workbench"
	"github.com/jarneson/fx-workbench/internal/workload"
)

type sidebarCatalog struct {
	entities []workload.Entity
	err      error
	calls    int
}

func (catalog *sidebarCatalog) List(workload.ListOptions) ([]workload.Entity, error) {
	catalog.calls++
	if catalog.err != nil {
		return nil, catalog.err
	}
	return append([]workload.Entity(nil), catalog.entities...), nil
}

type sidebarRuntime struct {
	snapshot runtimeworkbench.Snapshot
	err      error
	calls    int
	focused  runtimeworkbench.Session
}

func (runtime *sidebarRuntime) Snapshot(context.Context) (runtimeworkbench.Snapshot, error) {
	runtime.calls++
	return runtime.snapshot, runtime.err
}

func (runtime *sidebarRuntime) Focus(_ context.Context, session runtimeworkbench.Session) error {
	runtime.focused = session
	return runtime.err
}

type sidebarActions struct {
	action       string
	task         workload.Entity
	target       runtimeworkbench.Session
	restoreAgent bool
	err          error
}

func (actions *sidebarActions) Focus(_ context.Context, task workload.Entity, target runtimeworkbench.Session) error {
	actions.action, actions.task, actions.target = "focus", task, target
	return actions.err
}

func (actions *sidebarActions) Resume(_ context.Context, task workload.Entity) error {
	actions.action, actions.task = "resume", task
	return actions.err
}

func (actions *sidebarActions) Review(_ context.Context, task workload.Entity, target runtimeworkbench.Session) error {
	actions.action, actions.task, actions.target = "review", task, target
	return actions.err
}

func (actions *sidebarActions) OpenPullRequest(_ context.Context, task workload.Entity, restoreAgent bool) error {
	actions.action, actions.task, actions.restoreAgent = "pull-request", task, restoreAgent
	return actions.err
}

func sidebarTasks() []workload.Entity {
	return []workload.Entity{
		{Type: "workstream", ID: "core", Project: "demo", Title: "Core runtime"},
		{Type: "task", ID: "later", Project: "demo", Workstream: "core", Title: "A very long later task title", Status: "ready", Order: 20, Repository: "/code/demo", Branch: "later"},
		{Type: "task", ID: "first", Project: "demo", Workstream: "core", Title: "First", Status: "active", Order: 10, Repository: "/code/demo", Branch: "first", FXSessionID: "fx-first"},
	}
}

func loadSidebar(t *testing.T, catalog *sidebarCatalog, runtime *sidebarRuntime) SidebarModel {
	t.Helper()
	model := NewSidebarModel(catalog, runtime, "demo", "core", time.Second)
	updated, _ := model.Update(model.load()())
	model = updated.(SidebarModel)
	model.width, model.height = 32, 20
	return model
}

func TestSidebarCompactRenderingSeparatesWorkflowAndRuntimeState(t *testing.T) {
	catalog := &sidebarCatalog{entities: sidebarTasks()}
	runtime := &sidebarRuntime{snapshot: runtimeworkbench.Snapshot{Sessions: []runtimeworkbench.Session{{
		ID: "$1", Name: "shared", Windows: []runtimeworkbench.Window{{ID: "@1", Panes: []runtimeworkbench.Pane{{
			ID: "%1", TaskID: "first", FXSessionID: "fx-first", AgentState: runtimeworkbench.StateIdle, NeedsAttention: true,
		}}}},
	}}}}
	model := loadSidebar(t, catalog, runtime)
	view := model.View()
	for _, expected := range []string{"DEMO", "Core Runtime", "CODING", "SELF REVIEW", "IN REVIEW", "READY", "BLOCKED", "MERGED", "10 First", "20 A very long later t…", "!", "×"} {
		if !strings.Contains(view, expected) {
			t.Fatalf("view missing %q:\n%s", expected, view)
		}
	}
	if strings.Contains(view, "status:") || strings.Contains(view, "runtime:") {
		t.Fatalf("view still renders verbose status rows:\n%s", view)
	}
	if strings.Index(view, "CODING") > strings.Index(view, "READY") {
		t.Fatalf("workflow sections are out of order:\n%s", view)
	}
	for _, line := range strings.Split(view, "\n") {
		if width := lipgloss.Width(line); width > model.width {
			t.Fatalf("line width %d exceeds %d: %q", width, model.width, line)
		}
	}
}

func TestSidebarOrdersRequestedWorkflowSections(t *testing.T) {
	entities := []workload.Entity{
		{Type: "workstream", ID: "core", Project: "demo", Title: "Core runtime"},
		{Type: "task", ID: "merged", Project: "demo", Workstream: "core", Title: "Merged task", Status: "merged", Order: 1},
		{Type: "task", ID: "blocked", Project: "demo", Workstream: "core", Title: "Blocked task", Status: "blocked", Order: 1},
		{Type: "task", ID: "ready", Project: "demo", Workstream: "core", Title: "Ready task", Status: "ready", Order: 1},
		{Type: "task", ID: "active", Project: "demo", Workstream: "core", Title: "Active task", Status: "active", Order: 1},
	}
	model := loadSidebar(t, &sidebarCatalog{entities: entities}, &sidebarRuntime{})
	view := model.View()
	lines := strings.Split(view, "\n")
	last := -1
	for _, section := range []string{"CODING", "SELF REVIEW", "IN REVIEW", "READY", "BLOCKED", "MERGED"} {
		index := slices.Index(lines, section)
		if index < 0 || index <= last {
			t.Fatalf("section %s missing or out of order:\n%s", section, view)
		}
		last = index
	}
	for _, section := range []string{"READY", "BLOCKED", "MERGED"} {
		if !strings.Contains(view, "\n\n"+section) {
			t.Fatalf("section %s is missing preceding spacing:\n%s", section, view)
		}
	}
}

func TestSidebarShowsEmptySectionsWithSpacing(t *testing.T) {
	entities := []workload.Entity{{Type: "workstream", ID: "core", Project: "demo", Title: "Core runtime"}}
	model := loadSidebar(t, &sidebarCatalog{entities: entities}, &sidebarRuntime{})
	view := model.View()
	for _, expected := range []string{"CODING\n\nSELF REVIEW", "SELF REVIEW\n\nIN REVIEW", "IN REVIEW\n\nREADY", "READY\n\nBLOCKED", "BLOCKED\n\nMERGED", "MERGED\n\nNo tasks"} {
		if !strings.Contains(view, expected) {
			t.Fatalf("sidebar missing empty section spacing %q:\n%s", expected, view)
		}
	}
}

func TestSidebarDoesNotSelectMergedTasks(t *testing.T) {
	entities := []workload.Entity{
		{Type: "workstream", ID: "core", Project: "demo", Title: "Core runtime"},
		{Type: "task", ID: "merged", Project: "demo", Workstream: "core", Title: "Merged task", Status: "merged", Order: 1},
		{Type: "task", ID: "ready", Project: "demo", Workstream: "core", Title: "Ready task", Status: "ready", Order: 1},
		{Type: "task", ID: "active", Project: "demo", Workstream: "core", Title: "Active task", Status: "active", Order: 1},
	}
	catalog := &sidebarCatalog{entities: entities}
	model := loadSidebar(t, catalog, &sidebarRuntime{})
	if task, ok := model.selectedTask(); !ok || task.ID != "active" {
		t.Fatalf("initial selection = %#v, %v", task, ok)
	}
	model.move(1)
	if task, ok := model.selectedTask(); !ok || task.ID != "ready" {
		t.Fatalf("next selection = %#v, %v", task, ok)
	}
	model.move(1)
	if task, _ := model.selectedTask(); task.ID != "ready" {
		t.Fatalf("navigation selected merged task: %#v", task)
	}

	catalog.entities = []workload.Entity{entities[0], entities[1]}
	updated, _ := model.Update(model.load()())
	model = updated.(SidebarModel)
	if _, ok := model.selectedTask(); ok || model.selected != -1 || strings.Contains(model.View(), "> ") {
		t.Fatalf("merged-only sidebar retained a selection:\n%s", model.View())
	}
}

func TestSidebarRenderingKeepsStylesBoundedAtVeryNarrowWidths(t *testing.T) {
	model := loadSidebar(t, &sidebarCatalog{entities: sidebarTasks()}, &sidebarRuntime{})
	for _, width := range []int{1, 4, 12} {
		model.width = width
		for _, line := range strings.Split(model.View(), "\n") {
			if got := lipgloss.Width(line); got > width {
				t.Fatalf("width %d emitted line width %d: %q", width, got, line)
			}
			if strings.Contains(line, "\x1b[") && !strings.Contains(line, "\x1b[0m") {
				t.Fatalf("width %d emitted unterminated style: %q", width, line)
			}
		}
	}
}

func TestSidebarRefreshPreservesSelectionAndLastSuccessfulDataOnErrors(t *testing.T) {
	catalog := &sidebarCatalog{entities: sidebarTasks()}
	runtime := &sidebarRuntime{}
	model := loadSidebar(t, catalog, runtime)
	model.selected = 1

	catalog.entities = append([]workload.Entity{{Type: "task", ID: "new", Project: "demo", Workstream: "core", Title: "New", Status: "draft", Order: 5}}, sidebarTasks()...)
	updated, _ := model.Update(model.load()())
	model = updated.(SidebarModel)
	if task, _ := model.selectedTask(); task.ID != "later" {
		t.Fatalf("selected task = %s, want later", task.ID)
	}

	catalog.err = errors.New("vault unavailable")
	runtime.err = errors.New("tmux unavailable")
	updated, _ = model.Update(model.load()())
	model = updated.(SidebarModel)
	if len(model.tasks) != 3 || !strings.Contains(model.View(), "catalog refresh failed") || !strings.Contains(model.View(), "runtime refresh failed") {
		t.Fatalf("failed refresh blanked data or errors:\n%s", model.View())
	}
}

func TestSidebarAutoRefreshUsesOneCatalogAndSnapshotCallPerCycle(t *testing.T) {
	catalog := &sidebarCatalog{entities: sidebarTasks()}
	runtime := &sidebarRuntime{}
	model := NewSidebarModel(catalog, runtime, "demo", "core", time.Nanosecond)
	updated, tick := model.Update(model.Init()())
	model = updated.(SidebarModel)
	if catalog.calls != 1 || runtime.calls != 1 || tick == nil {
		t.Fatalf("initial calls catalog=%d runtime=%d tick=%v", catalog.calls, runtime.calls, tick != nil)
	}
	updated, refresh := model.Update(tick())
	model = updated.(SidebarModel)
	if refresh == nil || !model.loading {
		t.Fatal("tick did not begin refresh")
	}
	updated, _ = model.Update(refresh())
	model = updated.(SidebarModel)
	if catalog.calls != 2 || runtime.calls != 2 {
		t.Fatalf("refresh calls catalog=%d runtime=%d", catalog.calls, runtime.calls)
	}
}

func TestTaskCorrelationPrecedenceAndFallbacks(t *testing.T) {
	task := workload.Entity{ID: "ship", FXSessionID: "fx-ship", Repository: "/code/demo", Branch: "ship"}
	snapshot := runtimeworkbench.Snapshot{Sessions: []runtimeworkbench.Session{
		{ID: "$legacy", Name: "legacy", Repository: "demo", Worktree: "/trees/demo/ship"},
		{ID: "$fx", Name: "fx", Windows: []runtimeworkbench.Window{{ID: "@fx", Panes: []runtimeworkbench.Pane{{ID: "%fx", FXSessionID: "fx-ship"}}}}},
		{ID: "$tag", Name: "tag", Windows: []runtimeworkbench.Window{{ID: "@tag", Panes: []runtimeworkbench.Pane{{ID: "%tag", TaskID: "ship"}}}}},
	}}
	target, ok := matchTaskSession(snapshot, task)
	if !ok || target.ID != "$tag" || target.Windows[0].Panes[0].ID != "%tag" {
		t.Fatalf("tag target = %#v, %v", target, ok)
	}

	snapshot.Sessions = snapshot.Sessions[:2]
	target, ok = matchTaskSession(snapshot, task)
	if !ok || target.ID != "$fx" {
		t.Fatalf("fx session target = %#v, %v", target, ok)
	}

	task.FXSessionID = ""
	snapshot.Sessions = snapshot.Sessions[:1]
	target, ok = matchTaskSession(snapshot, task)
	if !ok || target.ID != "$legacy" {
		t.Fatalf("legacy target = %#v, %v", target, ok)
	}
}

func TestSidebarEnterFocusesExactPaneForSeparateAndSharedTopologies(t *testing.T) {
	tests := []struct {
		name     string
		snapshot runtimeworkbench.Snapshot
		window   string
		pane     string
	}{
		{
			name:     "separate task session",
			snapshot: runtimeworkbench.Snapshot{Sessions: []runtimeworkbench.Session{{ID: "$task", Name: "task", TaskID: "first", Windows: []runtimeworkbench.Window{{ID: "@task", Panes: []runtimeworkbench.Pane{{ID: "%task", AgentState: runtimeworkbench.StateRunning}}}}}}},
			window:   "@task", pane: "%task",
		},
		{
			name: "shared workstream session",
			snapshot: runtimeworkbench.Snapshot{Sessions: []runtimeworkbench.Session{{ID: "$shared", Name: "shared", Windows: []runtimeworkbench.Window{
				{ID: "@other", Panes: []runtimeworkbench.Pane{{ID: "%other", TaskID: "other", AgentState: runtimeworkbench.StateRunning}}},
				{ID: "@first", Panes: []runtimeworkbench.Pane{{ID: "%first", TaskID: "first", FXSessionID: "fx-first", AgentState: runtimeworkbench.StateUnknown}}},
			}}}},
			window: "@first", pane: "%first",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			catalog := &sidebarCatalog{entities: sidebarTasks()}
			runtime := &sidebarRuntime{snapshot: test.snapshot}
			model := loadSidebar(t, catalog, runtime)
			updated, command := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
			model = updated.(SidebarModel)
			if command == nil {
				t.Fatal("focus command missing")
			}
			updated, next := model.Update(command())
			model = updated.(SidebarModel)
			if next != nil || model.focusing || runtime.focused.ID == "" || len(runtime.focused.Windows) != 1 || runtime.focused.Windows[0].ID != test.window || runtime.focused.Windows[0].Panes[0].ID != test.pane {
				t.Fatalf("focused target = %#v, next=%v focusing=%v", runtime.focused, next != nil, model.focusing)
			}
			if test.window == "@first" && runtime.focused.Windows[0].Panes[0].AgentState != runtimeworkbench.StateIdle {
				t.Fatalf("correlated startup pane not focusable: %#v", runtime.focused.Windows[0].Panes[0])
			}
		})
	}
}

func TestSidebarDispatchesOfflineAndReviewActions(t *testing.T) {
	tests := []struct {
		name             string
		status           string
		withTarget       bool
		wantAction       string
		wantRestoreAgent bool
	}{
		{name: "coding without live worker", status: "coding", wantAction: "resume"},
		{name: "self review without live worker", status: "self-review", wantAction: "review"},
		{name: "in review without live worker", status: "in-review", wantAction: "pull-request", wantRestoreAgent: true},
		{name: "in review with live worker", status: "in-review", withTarget: true, wantAction: "pull-request"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			task := workload.Entity{Type: "task", ID: "review-me", Project: "demo", Workstream: "core", Title: "Review me", Status: test.status, Branch: "review-me", Repository: "/code/demo"}
			catalog := &sidebarCatalog{entities: []workload.Entity{{Type: "workstream", ID: "core", Project: "demo", Title: "Core"}, task}}
			runtime := &sidebarRuntime{}
			if test.withTarget {
				runtime.snapshot = runtimeworkbench.Snapshot{Sessions: []runtimeworkbench.Session{{ID: "$shared", Windows: []runtimeworkbench.Window{{ID: "@task", Panes: []runtimeworkbench.Pane{{ID: "%task", TaskID: task.ID, FXSessionID: "fx-review-me", AgentState: runtimeworkbench.StateIdle}}}}}}}
			}
			actions := &sidebarActions{}
			model := loadSidebar(t, catalog, runtime).WithActions(actions)
			updated, command := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
			model = updated.(SidebarModel)
			if command == nil {
				t.Fatal("status action command missing")
			}
			updated, _ = model.Update(command())
			model = updated.(SidebarModel)
			if actions.action != test.wantAction || actions.task.ID != task.ID || actions.restoreAgent != test.wantRestoreAgent || model.focusing {
				t.Fatalf("action=%q task=%q restoreAgent=%v focusing=%v", actions.action, actions.task.ID, actions.restoreAgent, model.focusing)
			}
		})
	}
}

func TestSidebarRetriesCorrelatedStartupPane(t *testing.T) {
	task := workload.Entity{Type: "task", ID: "startup", Project: "demo", Workstream: "core", Title: "Startup", Status: "coding", Branch: "startup", Repository: "/code/demo", FXSessionID: "stale-session"}
	catalog := &sidebarCatalog{entities: []workload.Entity{{Type: "workstream", ID: "core", Project: "demo", Title: "Core"}, task}}
	runtime := &sidebarRuntime{snapshot: runtimeworkbench.Snapshot{Sessions: []runtimeworkbench.Session{{
		ID: "$shared", Windows: []runtimeworkbench.Window{{ID: "@startup", Panes: []runtimeworkbench.Pane{{ID: "%startup", TaskID: task.ID}}}},
	}}}}
	actions := &sidebarActions{}
	model := loadSidebar(t, catalog, runtime).WithActions(actions)
	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(SidebarModel)
	if command == nil {
		t.Fatal("startup retry command missing")
	}
	updated, _ = model.Update(command())
	model = updated.(SidebarModel)
	if actions.action != "resume" || runtime.focused.ID != "" || model.focusing {
		t.Fatalf("action=%q focused=%q focusing=%v, want resume through startup readiness", actions.action, runtime.focused.ID, model.focusing)
	}
}

func TestSidebarOfflineCodingReportsUnavailableResumeAction(t *testing.T) {
	model := loadSidebar(t, &sidebarCatalog{entities: sidebarTasks()}, &sidebarRuntime{})
	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(SidebarModel)
	if command == nil {
		t.Fatal("resume command missing")
	}
	updated, _ = model.Update(command())
	model = updated.(SidebarModel)
	if !strings.Contains(model.notice, "resume action is unavailable") || model.focusing {
		t.Fatalf("notice=%q focusing=%v", model.notice, model.focusing)
	}
}
