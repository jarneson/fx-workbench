package projectclose

import (
	"context"
	"strings"
	"testing"

	"github.com/jarneson/fx-workbench/internal/cleanup"
	"github.com/jarneson/fx-workbench/internal/workload"
)

type fakeCatalog struct {
	project     workload.Entity
	workstreams []workload.Entity
	tasks       []workload.Entity
	closed      *bool
}

func (catalog *fakeCatalog) FindType(_, _, entityType string) (workload.Entity, error) {
	if entityType == "project" {
		return catalog.project, nil
	}
	return workload.Entity{}, nil
}
func (catalog *fakeCatalog) List(options workload.ListOptions) ([]workload.Entity, error) {
	if options.Type == "workstream" {
		return catalog.workstreams, nil
	}
	if options.Type == "task" {
		return catalog.tasks, nil
	}
	return nil, nil
}
func (catalog *fakeCatalog) SetProjectClosed(_ workload.Entity, _ []workload.Entity, closed bool) error {
	catalog.closed = &closed
	return nil
}

type fakeRuntime struct {
	worktrees []cleanup.WorktreePlan
	sessions  []cleanup.Session
	current   string
	killed    []string
}

func (runtime *fakeRuntime) ScanWorktrees(context.Context, string, []workload.Entity) ([]cleanup.WorktreePlan, error) {
	return runtime.worktrees, nil
}
func (runtime *fakeRuntime) Sessions(context.Context) ([]cleanup.Session, error) {
	return runtime.sessions, nil
}
func (runtime *fakeRuntime) CurrentSession(context.Context) string { return runtime.current }
func (runtime *fakeRuntime) KillSession(_ context.Context, session cleanup.Session) error {
	runtime.killed = append(runtime.killed, session.ID)
	return nil
}

func closeoutCatalog(status string) *fakeCatalog {
	return &fakeCatalog{
		project:     workload.Entity{Schema: workload.SchemaV1, Type: "project", ID: "demo", Project: "demo", Status: "active", RootRepository: "/repo", Repositories: []string{"/repo"}},
		workstreams: []workload.Entity{{Schema: workload.SchemaV1, Type: "workstream", ID: "core", Project: "demo", Status: "active"}},
		tasks:       []workload.Entity{{Schema: workload.SchemaV1, Type: "task", ID: "ship", Project: "demo", Status: status, Repository: "/repo", Branch: "ship"}},
	}
}

func TestBuildAndExecuteEligibleCloseout(t *testing.T) {
	catalog := closeoutCatalog("merged")
	runtime := &fakeRuntime{}
	plan, err := Build(context.Background(), catalog, runtime, "demo", false)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Eligible {
		t.Fatalf("plan blocked: %v", plan.Blockers)
	}
	result, err := Execute(context.Background(), catalog, runtime, plan)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Closed || catalog.closed == nil || !*catalog.closed {
		t.Fatalf("project not closed: %#v", result)
	}
}

func TestBuildBlocksDoneTasksAndWorktrees(t *testing.T) {
	catalog := closeoutCatalog("done")
	runtime := &fakeRuntime{worktrees: []cleanup.WorktreePlan{{TaskID: "ship", Path: "/trees/ship"}}}
	plan, err := Build(context.Background(), catalog, runtime, "demo", false)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Eligible {
		t.Fatal("blocked project unexpectedly eligible")
	}
	joined := strings.Join(plan.Blockers, "; ")
	if !strings.Contains(joined, "done=1") || !strings.Contains(joined, "task worktree remains") {
		t.Fatalf("unexpected blockers: %v", plan.Blockers)
	}
}

func TestCloseSessionsMustBeExplicit(t *testing.T) {
	catalog := closeoutCatalog("merged")
	runtime := &fakeRuntime{sessions: []cleanup.Session{{ID: "$1", Name: "root-demo", Role: "root", Repository: "/repo"}}}
	blocked, err := Build(context.Background(), catalog, runtime, "demo", false)
	if err != nil {
		t.Fatal(err)
	}
	if blocked.Eligible || !strings.Contains(strings.Join(blocked.Blockers, "; "), "--close-sessions") {
		t.Fatalf("session did not block: %#v", blocked)
	}
	plan, err := Build(context.Background(), catalog, runtime, "demo", true)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Eligible {
		t.Fatalf("close-sessions plan blocked: %v", plan.Blockers)
	}
	result, err := Execute(context.Background(), catalog, runtime, plan)
	if err != nil {
		t.Fatal(err)
	}
	if result.SessionsClosed != 1 || len(runtime.killed) != 1 {
		t.Fatalf("session not closed: %#v", result)
	}
}

func TestCloseSessionsRefusesCurrentSession(t *testing.T) {
	catalog := closeoutCatalog("merged")
	runtime := &fakeRuntime{
		current:  "$1",
		sessions: []cleanup.Session{{ID: "$1", Name: "root-demo", Role: "root", Repository: "/repo"}},
	}
	plan, err := Build(context.Background(), catalog, runtime, "demo", true)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Eligible || !strings.Contains(strings.Join(plan.Blockers, "; "), "current tmux session") {
		t.Fatalf("current session did not block: %#v", plan)
	}
}

func TestReopenClosedProject(t *testing.T) {
	catalog := closeoutCatalog("merged")
	catalog.project.Status = "closed"
	if err := Reopen(catalog, "demo"); err != nil {
		t.Fatal(err)
	}
	if catalog.closed == nil || *catalog.closed {
		t.Fatal("project was not reopened")
	}
}
