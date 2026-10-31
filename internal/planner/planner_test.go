package planner

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jarneson/fx-workbench/internal/mainsync"
	"github.com/jarneson/fx-workbench/internal/workload"
)

type fakeCatalog struct {
	resolution workload.ProjectResolution
	entities   []workload.Entity
	err        error
	persisted  *string
}

func (catalog fakeCatalog) ResolveProject(_, _ string) (workload.ProjectResolution, error) {
	return catalog.resolution, catalog.err
}

func (catalog fakeCatalog) List(workload.ListOptions) ([]workload.Entity, error) {
	return catalog.entities, catalog.err
}
func (catalog fakeCatalog) SetWorkstreamPlannerSession(_ workload.Entity, sessionID string) error {
	if catalog.persisted != nil {
		*catalog.persisted = sessionID
	}
	return catalog.err
}

type fakeSync struct {
	calls  int
	events *[]string
}

func (syncer *fakeSync) Sync(_ context.Context, repository string) (mainsync.Result, error) {
	syncer.calls++
	if syncer.events != nil {
		*syncer.events = append(*syncer.events, "sync")
	}
	return mainsync.Result{MainWorktree: repository}, nil
}

type fakeRuntime struct {
	calls  int
	result LaunchResult
	events *[]string
}

func (runtime *fakeRuntime) Ensure(_ context.Context, plan Plan) (LaunchResult, error) {
	runtime.calls++
	if runtime.events != nil {
		*runtime.events = append(*runtime.events, "ensure")
	}
	if runtime.result.SessionName == "" {
		runtime.result = LaunchResult{SessionName: plan.SessionName, CreatedSession: true, CreatedPlanner: true}
	}
	return runtime.result, nil
}

func TestExecuteDryRunBuildsCanonicalPromptWithoutRuntime(t *testing.T) {
	root := t.TempDir()
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	catalog := fakeCatalog{
		resolution: workload.ProjectResolution{Project: workload.Entity{
			Schema: workload.SchemaV1, Type: "project", ID: "demo", Title: "Demo",
			Status: "active", Project: "demo", RootRepository: root,
			Repositories: []string{root, "/tmp/service"}, SourcePath: "/vault/Projects/demo/project.md",
		}},
		entities: []workload.Entity{
			{Schema: workload.SchemaV1, Type: "workstream", ID: "core", Title: "Core", Status: "active", Order: 1, SourcePath: "/vault/Projects/demo/01-core/01-core.md"},
			{Schema: workload.SchemaV1, Type: "task", ID: "ship", Title: "Ship", Status: "ready", Workstream: "core", Repository: root},
			{Schema: workload.SchemaV1, Type: "task", ID: "old", Title: "Old", Status: "merged", Workstream: "core", Repository: root},
			{Schema: workload.SchemaV1, Type: "task", ID: "awaiting-merge", Title: "Awaiting merge", Status: "done", Workstream: "core", Repository: root},
		},
	}
	runtime := &fakeRuntime{}
	syncer := &fakeSync{}
	plan, _, err := Execute(context.Background(), catalog, syncer, runtime, root, "demo", "core", true)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.calls != 0 || syncer.calls != 0 {
		t.Fatalf("dry-run called sync %d times and runtime %d times", syncer.calls, runtime.calls)
	}
	if !strings.HasPrefix(plan.SessionName, "tasks-core-") {
		t.Fatalf("workstream session name = %q", plan.SessionName)
	}
	if plan.WindowName != "plan-core" {
		t.Fatalf("planner window name = %q", plan.WindowName)
	}
	for _, expected := range []string{
		"Collaborative planning session context for workstream core (Core)",
		"WORKBENCH AGENT PROFILE planner",
		"Build the Foundation First",
		"Separate Before You Lock",
		"No planning action is requested by this bootstrap message",
		"do not call tools, read files, investigate the repository, change planning metadata, or make recommendations",
		"ask the user what they would like to plan together",
		"Planning completion alone is not approval to start implementation",
		"do not launch tasks, create branches/worktrees, or modify repository/runtime state automatically",
		"If the user explicitly asks to start a specific task or explicitly approves a proposed launch",
		"fx-workbench launch or the worktree-task-launcher skill",
		"Never infer approval or launch unrelated tasks",
		"Until the user provides initial direction, remain idle",
		"Project manifest: /vault/Projects/demo/project.md",
		"Workstream packet: /vault/Projects/demo/01-core/01-core.md",
		"Target repository: " + canonicalRoot,
		"Repository registry:",
		"- core [active]: Core",
		"- core / ship [ready]: Ship",
		"- core / awaiting-merge [done]: Awaiting merge",
		"obsidian-planner skill",
		"fx-workbench list/show/scaffold/status",
	} {
		if !strings.Contains(plan.Prompt, expected) {
			t.Errorf("prompt missing %q:\n%s", expected, plan.Prompt)
		}
	}
	if strings.Contains(plan.Prompt, "core / old [merged]") {
		t.Fatalf("completed task leaked into prompt: %s", plan.Prompt)
	}
	if strings.HasPrefix(plan.Prompt, "Plan workstream") {
		t.Fatalf("bootstrap prompt still issues an autonomous planning command: %s", plan.Prompt)
	}
	if strings.Contains(plan.Prompt, "Do not launch implementation tasks, create branches/worktrees, or modify repository state from this planning session") {
		t.Fatalf("bootstrap prompt still absolutely prohibits explicitly approved launches: %s", plan.Prompt)
	}
}

func TestExecutePersistsPlannerSessionOnWorkstream(t *testing.T) {
	root := t.TempDir()
	persisted := ""
	catalog := fakeCatalog{
		persisted: &persisted,
		resolution: workload.ProjectResolution{Project: workload.Entity{
			Schema: workload.SchemaV1, Type: "project", ID: "demo", Title: "Demo", Status: "active",
			Project: "demo", RootRepository: root, Repositories: []string{root}, SourcePath: "/vault/demo/project.md",
		}},
		entities: []workload.Entity{{
			Schema: workload.SchemaV1, Type: "workstream", ID: "core", Title: "Core", Status: "active",
			Project: "demo", Repository: root, SourcePath: "/vault/demo/01-core/workstream.md",
		}},
	}
	events := []string{}
	runtime := &fakeRuntime{result: LaunchResult{SessionName: "api", PaneID: "%1", FXSessionID: "fx-session-123"}, events: &events}
	syncer := &fakeSync{events: &events}
	_, result, err := Execute(context.Background(), catalog, syncer, runtime, root, "demo", "core", false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(events, ",") != "sync,ensure" || result.MainWorktree == "" {
		t.Fatalf("planner order/result = %v, %#v", events, result)
	}
	if persisted != "fx-session-123" {
		t.Fatalf("persisted session = %q", persisted)
	}
}

func TestBuildRejectsLegacyProjectWithMigrationCommand(t *testing.T) {
	catalog := fakeCatalog{resolution: workload.ProjectResolution{Project: workload.Entity{
		Type: "project", ID: "legacy", Title: "Legacy", Legacy: true,
	}}}
	_, err := Build(catalog, t.TempDir(), "legacy", "")
	if err == nil || !strings.Contains(err.Error(), "fx-workbench project onboard") {
		t.Fatalf("error = %v, want migration command", err)
	}
}
