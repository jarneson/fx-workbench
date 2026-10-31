package taskui

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/jarneson/fx-workbench/internal/tasklaunch"
	runtimeworkbench "github.com/jarneson/fx-workbench/internal/workbench"
	"github.com/jarneson/fx-workbench/internal/workload"
)

type fakeCatalog struct {
	entities []workload.Entity
	err      error
}

func (catalog fakeCatalog) List(options workload.ListOptions) ([]workload.Entity, error) {
	if catalog.err != nil {
		return nil, catalog.err
	}
	var result []workload.Entity
	for _, entity := range catalog.entities {
		if options.Type != "" && entity.Type != options.Type {
			continue
		}
		if options.Project != "" && entity.Project != options.Project && entity.ID != options.Project {
			continue
		}
		result = append(result, entity)
	}
	return result, nil
}

type fakeRuntime struct {
	snapshot runtimeworkbench.Snapshot
	err      error
	focused  *string
}

func (runtime fakeRuntime) Snapshot(context.Context) (runtimeworkbench.Snapshot, error) {
	return runtime.snapshot, runtime.err
}
func (runtime fakeRuntime) Focus(_ context.Context, session runtimeworkbench.Session) error {
	if runtime.focused != nil {
		*runtime.focused = session.Name
	}
	return runtime.err
}

type fakeActions struct {
	planner, inspected, preflighted, launched []string
	err                                       error
}

func (actions *fakeActions) OpenPlanner(_ context.Context, entity workload.Entity) (string, error) {
	actions.planner = append(actions.planner, entity.ID)
	return "planner focused", actions.err
}
func (actions *fakeActions) Inspect(_ context.Context, entity workload.Entity) (string, error) {
	actions.inspected = append(actions.inspected, entity.ID)
	return "source closed", actions.err
}
func (actions *fakeActions) Preflight(_ context.Context, entity workload.Entity) (string, error) {
	actions.preflighted = append(actions.preflighted, entity.ID)
	return "dry-run: launch preflight passed", actions.err
}
func (actions *fakeActions) Launch(_ context.Context, entity workload.Entity) (string, error) {
	actions.launched = append(actions.launched, entity.ID)
	return "launched", actions.err
}

type fakeTaskLauncher struct {
	launched []string
}

func (launcher *fakeTaskLauncher) Launch(_ context.Context, task workload.Entity, _ string, report func(tasklaunch.Progress)) (tasklaunch.Result, error) {
	launcher.launched = append(launcher.launched, task.ID)
	report(tasklaunch.Progress{Stage: tasklaunch.StageValidate, State: tasklaunch.EventStarted, Message: "validating"})
	report(tasklaunch.Progress{Stage: tasklaunch.StageValidate, State: tasklaunch.EventCompleted, Message: "validated"})
	return tasklaunch.Result{Target: tasklaunch.Target{Worktree: "/trees/second", Session: "task-second"}, FXPane: "%2"}, nil
}

func canonicalEntities() []workload.Entity {
	return []workload.Entity{
		{Schema: workload.SchemaV1, Type: "project", ID: "demo", Project: "demo", Title: "Demo", Status: "active", RootRepository: "/code/demo", SourcePath: "/vault/demo/project.md"},
		{Schema: workload.SchemaV1, Type: "workstream", ID: "core", Project: "demo", Title: "Core", Status: "active", SourcePath: "/vault/demo/01-core.md"},
		{Schema: workload.SchemaV1, Type: "task", ID: "first", Project: "demo", Workstream: "core", Title: "First", Status: "merged", Repository: "/code/demo", SourcePath: "/vault/demo/01-first.md"},
		{Schema: workload.SchemaV1, Type: "task", ID: "second", Project: "demo", Workstream: "core", Title: "Second", Status: "ready", Repository: "/code/demo", Branch: "second", DependsOn: []string{"first"}, SourcePath: "/vault/demo/02-second.md"},
	}
}

func loadedModel(t *testing.T, catalog fakeCatalog, actions *fakeActions, runtime Runtime) Model {
	t.Helper()
	model := NewModel(catalog, actions, runtime, "")
	updated, _ := model.Update(model.load()())
	model = updated.(Model)
	model.width, model.height = 140, 40
	return model
}

func deliver(model Model, message tea.Msg) (Model, tea.Cmd) {
	updated, command := model.Update(message)
	return updated.(Model), command
}

func press(model Model, value string) (Model, tea.Cmd) {
	var message tea.KeyMsg
	if len([]rune(value)) == 1 {
		message = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(value)}
	} else {
		switch value {
		case "enter":
			message = tea.KeyMsg{Type: tea.KeyEnter}
		case "esc":
			message = tea.KeyMsg{Type: tea.KeyEsc}
		case "down":
			message = tea.KeyMsg{Type: tea.KeyDown}
		}
	}
	updated, command := model.Update(message)
	return updated.(Model), command
}

func TestDefaultModelResolvesCurrentRepositoryProject(t *testing.T) {
	vault := t.TempDir()
	repository := t.TempDir()
	if output, err := exec.Command("git", "init", "-q", "-b", "main", repository).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	t.Setenv("OBSIDIAN_VAULT", vault)
	t.Setenv("FX_WORKBENCH_PROJECT", "stale-selection")
	store := &workload.Store{Vault: vault}
	if _, err := store.Scaffold(workload.Entity{
		Type: "project", ID: "demo", Title: "Demo", Status: "active",
		RootRepository: repository, Repositories: []string{repository},
	}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Scaffold(workload.Entity{
		Type: "workstream", ID: "core", Title: "Core", Status: "active", Project: "demo",
	}, ""); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repository)
	model := NewDefaultModel("", nil)
	if model.projectOnly != "demo" {
		t.Fatalf("projectOnly = %q, want demo", model.projectOnly)
	}
	updated, _ := model.Update(model.load()())
	model = updated.(Model)
	if model.level != tasksLevel {
		t.Fatalf("level = %v, want tasks", model.level)
	}
}

func TestHierarchyNavigationBreadcrumbsAndRuntimeCorrelation(t *testing.T) {
	actions := &fakeActions{}
	model := loadedModel(t, fakeCatalog{entities: canonicalEntities()}, actions, fakeRuntime{snapshot: runtimeworkbench.Snapshot{Sessions: []runtimeworkbench.Session{{Name: "task-second", TaskID: "second", State: runtimeworkbench.StateRunning}}}})
	model, _ = press(model, "enter")
	if model.level != workstreamsLevel || !strings.Contains(model.View(), "Projects  ›  Demo") {
		t.Fatalf("project navigation failed:\n%s", model.View())
	}
	model, _ = press(model, "enter")
	view := model.View()
	for _, expected := range []string{"Projects  ›  Demo  ›  Core", "Second", "dependencies", "running", "task-second"} {
		if !strings.Contains(strings.ToLower(view), strings.ToLower(expected)) {
			t.Fatalf("view missing %q:\n%s", expected, view)
		}
	}
	model, _ = press(model, "esc")
	if model.level != workstreamsLevel {
		t.Fatalf("level=%d, want workstreams", model.level)
	}
}

func TestTasksOrderByWorkflowStatusThenStableOrder(t *testing.T) {
	entities := canonicalEntities()
	entities = append(entities,
		workload.Entity{Schema: workload.SchemaV1, Type: "task", ID: "coding", Project: "demo", Workstream: "core", Title: "Coding", Status: "coding", Order: 9},
		workload.Entity{Schema: workload.SchemaV1, Type: "task", ID: "self-review", Project: "demo", Workstream: "core", Title: "Self review", Status: "self-review", Order: 9},
		workload.Entity{Schema: workload.SchemaV1, Type: "task", ID: "in-review", Project: "demo", Workstream: "core", Title: "In review", Status: "in-review", Order: 9},
		workload.Entity{Schema: workload.SchemaV1, Type: "task", ID: "blocked", Project: "demo", Workstream: "core", Title: "Blocked", Status: "blocked", Order: 1},
		workload.Entity{Schema: workload.SchemaV1, Type: "task", ID: "superseded", Project: "demo", Workstream: "core", Title: "Superseded", Status: "superseded", Order: 2},
	)
	model := loadedModel(t, fakeCatalog{entities: entities}, &fakeActions{}, nil)
	model, _ = press(model, "enter")
	model, _ = press(model, "enter")
	tasks := model.visibleTasks()
	got := make([]string, len(tasks))
	for index, task := range tasks {
		got[index] = task.ID
	}
	want := []string{"coding", "self-review", "in-review", "second", "blocked", "first", "superseded"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("task order = %v, want %v", got, want)
	}
}

func TestLiveSessionFallsBackToLegacyWorktreeIdentity(t *testing.T) {
	model := Model{snapshot: runtimeworkbench.Snapshot{Sessions: []runtimeworkbench.Session{{
		Name: "legacy-task", Repository: "api", Worktree: "/worktrees/api/feature-branch",
	}}}}
	session, ok := model.liveSession(workload.Entity{ID: "task-id", Branch: "feature-branch", Repository: "/code/api"})
	if !ok || session.Name != "legacy-task" {
		t.Fatalf("legacy session not correlated: %#v, %t", session, ok)
	}
}

func TestLiveSessionSelectsExactWorkstreamTaskWindowByTaskOrFXSession(t *testing.T) {
	model := Model{snapshot: runtimeworkbench.Snapshot{Sessions: []runtimeworkbench.Session{{
		ID: "$1", Name: "tasks-core", Role: runtimeworkbench.RoleWorkstream,
		Windows: []runtimeworkbench.Window{
			{ID: "@1", TaskID: "first", FXSessionID: "fx-first"},
			{ID: "@2", TaskID: "second", FXSessionID: "fx-second"},
		},
	}}}}
	for _, task := range []workload.Entity{{ID: "second"}, {ID: "renamed", FXSessionID: "fx-second"}} {
		session, ok := model.liveSession(task)
		if !ok || len(session.Windows) != 1 || session.Windows[0].ID != "@2" {
			t.Fatalf("task %#v resolved to %#v, %t", task, session, ok)
		}
	}
}

func TestEnterFocusesLiveTaskSession(t *testing.T) {
	focused := ""
	runtime := fakeRuntime{
		focused: &focused,
		snapshot: runtimeworkbench.Snapshot{Sessions: []runtimeworkbench.Session{{
			ID: "$2", Name: "task-second", TaskID: "second", State: runtimeworkbench.StateRunning,
			Windows: []runtimeworkbench.Window{{ID: "@2", Panes: []runtimeworkbench.Pane{{ID: "%2", AgentState: runtimeworkbench.StateRunning, AgentTitle: "working"}}}},
		}}},
	}
	model := loadedModel(t, fakeCatalog{entities: canonicalEntities()}, &fakeActions{}, runtime)
	model, _ = press(model, "enter")
	model, _ = press(model, "enter")
	model, command := press(model, "enter")
	if command == nil || focused != "" {
		t.Fatalf("focus ran synchronously or command missing: focused=%q", focused)
	}
	updated, quit := deliver(model, command())
	if focused != "task-second" || quit == nil || updated.err != nil {
		t.Fatalf("focus result: focused=%q quit=%v err=%v", focused, quit != nil, updated.err)
	}
}

func TestPlannerPreflightAndConfirmedLaunchRunOnlyInCommands(t *testing.T) {
	actions := &fakeActions{}
	model := loadedModel(t, fakeCatalog{entities: canonicalEntities()}, actions, nil)
	var command tea.Cmd
	model, _ = press(model, "enter")
	model, command = press(model, "p")
	if command == nil || len(actions.planner) != 0 {
		t.Fatal("planner ran in Update")
	}
	model, quit := deliver(model, command())
	if len(actions.planner) != 1 || actions.planner[0] != "core" || quit == nil {
		t.Fatalf("planner calls=%v quit=%v", actions.planner, quit != nil)
	}

	model = loadedModel(t, fakeCatalog{entities: canonicalEntities()}, actions, nil)
	model, _ = press(model, "enter")
	model, command = press(model, "i")
	if command == nil || len(actions.inspected) != 0 {
		t.Fatal("source inspection ran in Update")
	}
	model, _ = deliver(model, command())
	if len(actions.inspected) != 1 {
		t.Fatalf("inspection calls=%v", actions.inspected)
	}
	model, _ = press(model, "enter")
	model, command = press(model, "c")
	if command == nil || len(actions.preflighted) != 0 {
		t.Fatal("preflight ran in Update")
	}
	model, _ = deliver(model, command())
	if !strings.Contains(model.notice, "preflight passed") {
		t.Fatalf("notice=%q", model.notice)
	}
	model, command = press(model, "L")
	if command != nil || !model.confirm {
		t.Fatal("launch did not request confirmation")
	}
	model, command = press(model, "y")
	if command == nil || len(actions.launched) != 0 {
		t.Fatal("launch ran before command")
	}
	message := command()
	model, command = func() (Model, tea.Cmd) { updated, next := model.Update(message); return updated.(Model), next }()
	if len(actions.launched) != 1 || command == nil {
		t.Fatalf("launch=%v refresh=%v", actions.launched, command != nil)
	}
}

func TestPlannerFailureKeepsTaskSelectorOpen(t *testing.T) {
	actions := &fakeActions{err: errors.New("planner failed")}
	model := loadedModel(t, fakeCatalog{entities: canonicalEntities()}, actions, nil)
	model, _ = press(model, "enter")
	model, command := press(model, "p")
	if command == nil {
		t.Fatal("planner command missing")
	}
	model, quit := deliver(model, command())
	if quit != nil || model.err == nil {
		t.Fatalf("quit=%v err=%v", quit != nil, model.err)
	}
}

func TestNativeLaunchStreamsProgressAndRefreshes(t *testing.T) {
	launcher := &fakeTaskLauncher{}
	model := loadedModel(t, fakeCatalog{entities: canonicalEntities()}, &fakeActions{}, nil)
	model.launcher = launcher
	model, _ = press(model, "enter")
	model, _ = press(model, "enter")
	model, command := press(model, "L")
	if command != nil || !model.confirm {
		t.Fatal("launch did not request confirmation")
	}
	model, command = press(model, "y")
	if command == nil || !model.launching || len(launcher.launched) != 0 {
		t.Fatalf("launch did not start asynchronously: %#v", model)
	}
	model, command = deliver(model, command())
	if !strings.Contains(model.View(), "LAUNCH PROGRESS") || !strings.Contains(model.View(), "validate") {
		t.Fatalf("progress not visible:\n%s", model.View())
	}
	for command != nil {
		model, command = deliver(model, command())
	}
	if len(launcher.launched) != 1 || model.launching || model.err != nil {
		t.Fatalf("launch=%v launching=%v err=%v", launcher.launched, model.launching, model.err)
	}
	if !strings.Contains(model.notice, "task-second") {
		t.Fatalf("notice=%q", model.notice)
	}
}

func TestInvalidLegacyTaskShowsDiagnosticsAndCannotLaunch(t *testing.T) {
	entities := canonicalEntities()
	entities[3].Schema = ""
	entities[3].Legacy = true
	entities[3].Diagnostics = []string{"missing repository", "legacy or missing schema"}
	model := loadedModel(t, fakeCatalog{entities: entities}, &fakeActions{}, nil)
	model, _ = press(model, "enter")
	model, _ = press(model, "enter")
	view := model.View()
	for _, expected := range []string{"LEGACY", "DIAGNOSTICS", "missing repository"} {
		if !strings.Contains(view, expected) {
			t.Fatalf("view missing %q:\n%s", expected, view)
		}
	}
	model, command := press(model, "L")
	if command != nil || model.confirm || model.err == nil || !strings.Contains(model.err.Error(), "legacy tasks") {
		t.Fatalf("invalid launch state: %#v", model)
	}
}

func TestEmptyAndErrorStates(t *testing.T) {
	model := loadedModel(t, fakeCatalog{}, &fakeActions{}, fakeRuntime{err: errors.New("tmux unavailable")})
	if !strings.Contains(model.View(), "No workloads at this level") || !strings.Contains(model.View(), "runtime activity unavailable") {
		t.Fatalf("empty/runtime-error view:\n%s", model.View())
	}
	model = NewModel(fakeCatalog{err: errors.New("vault unavailable")}, &fakeActions{}, nil, "")
	updated, _ := model.Update(model.load()())
	model = updated.(Model)
	model.width = 100
	if !strings.Contains(model.View(), "vault unavailable") {
		t.Fatalf("error view:\n%s", model.View())
	}
}
