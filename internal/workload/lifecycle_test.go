package workload

import (
	"os"
	"strings"
	"testing"
)

func TestSetWorkstreamPlannerSessionPersistsID(t *testing.T) {
	store := &Store{Vault: t.TempDir()}
	_, err := store.Scaffold(Entity{Type: "project", ID: "demo", Title: "Demo", Status: "active", RootRepository: "/tmp/demo"}, "")
	if err != nil {
		t.Fatal(err)
	}
	workstream, err := store.Scaffold(Entity{Type: "workstream", ID: "core", Title: "Core", Status: "active", Project: "demo"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetWorkstreamPlannerSession(workstream, "fx-session-123"); err != nil {
		t.Fatal(err)
	}
	loaded, err := parseFile(workstream.SourcePath, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.PlannerSessionID != "fx-session-123" {
		t.Fatalf("planner session id = %q", loaded.PlannerSessionID)
	}
}

func TestSetTaskLaunchPersistsStatusAndFXSessionID(t *testing.T) {
	store := &Store{Vault: t.TempDir()}
	if _, err := store.Scaffold(Entity{Type: "project", ID: "demo", Title: "Demo", Status: "active", RootRepository: "/tmp/demo"}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Scaffold(Entity{Type: "workstream", ID: "core", Title: "Core", Status: "active", Project: "demo"}, ""); err != nil {
		t.Fatal(err)
	}
	task, err := store.Scaffold(Entity{Type: "task", ID: "ship", Title: "Ship", Status: "ready", Project: "demo", Workstream: "core", Repository: "/tmp/demo"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetTaskLaunch(task, "fx-task-session-123"); err != nil {
		t.Fatal(err)
	}
	loaded, err := parseFile(task.SourcePath, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != "coding" || loaded.FXSessionID != "fx-task-session-123" {
		t.Fatalf("launched task = %#v", loaded)
	}
}

func TestSetTaskSessionPreservesSelfReviewStatus(t *testing.T) {
	store := &Store{Vault: t.TempDir()}
	if _, err := store.Scaffold(Entity{Type: "project", ID: "demo", Title: "Demo", Status: "active", RootRepository: "/tmp/demo"}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Scaffold(Entity{Type: "workstream", ID: "core", Title: "Core", Status: "active", Project: "demo"}, ""); err != nil {
		t.Fatal(err)
	}
	task, err := store.Scaffold(Entity{Type: "task", ID: "review", Title: "Review", Status: "self-review", Project: "demo", Workstream: "core", Repository: "/tmp/demo"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetTaskSession(task, "fx-review-session-123"); err != nil {
		t.Fatal(err)
	}
	loaded, err := parseFile(task.SourcePath, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != "self-review" || loaded.FXSessionID != "fx-review-session-123" {
		t.Fatalf("review task = %#v", loaded)
	}
}

func TestSetWorkstreamClosedFiltersWorkstreamAndTasks(t *testing.T) {
	store := &Store{Vault: t.TempDir()}
	_, err := store.Scaffold(Entity{Type: "project", ID: "demo", Title: "Demo", Status: "active", RootRepository: "/tmp/demo"}, "")
	if err != nil {
		t.Fatal(err)
	}
	workstream, err := store.Scaffold(Entity{Type: "workstream", ID: "core", Title: "Core", Status: "active", Project: "demo"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Scaffold(Entity{Type: "task", ID: "ship", Title: "Ship", Status: "merged", Project: "demo", Workstream: "core", Repository: "/tmp/demo"}, ""); err != nil {
		t.Fatal(err)
	}
	if err := store.SetWorkstreamClosed(workstream, true); err != nil {
		t.Fatal(err)
	}
	entities, err := store.List(ListOptions{Project: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	if len(entities) != 1 || entities[0].Type != "project" {
		t.Fatalf("closed workstream leaked into default list: %#v", entities)
	}
	tasks, err := store.List(ListOptions{Type: "task", Project: "demo", Workstream: "core"})
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Fatalf("explicit closed workstream query lost tasks: %#v", tasks)
	}
	workstreams, err := store.List(ListOptions{Type: "workstream", Project: "demo", IncludeClosed: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(workstreams) != 1 || workstreams[0].Status != "closed" || workstreams[0].ClosedAt == "" {
		t.Fatalf("closed workstream unavailable with all: %#v", workstreams)
	}
}

func TestSetProjectClosedFiltersAndReopensProject(t *testing.T) {
	store := &Store{Vault: t.TempDir()}
	project, err := store.Scaffold(Entity{
		Type: "project", ID: "demo", Title: "Demo", Status: "active", RootRepository: "/tmp/demo",
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	workstream, err := store.Scaffold(Entity{
		Type: "workstream", ID: "core", Title: "Core", Status: "active", Project: "demo",
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetProjectClosed(project, []Entity{workstream}, true); err != nil {
		t.Fatal(err)
	}
	projects, err := store.List(ListOptions{Type: "project"})
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 0 {
		t.Fatalf("closed project visible by default: %#v", projects)
	}
	projects, err = store.List(ListOptions{Type: "project", IncludeClosed: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 || projects[0].Status != "closed" || projects[0].ClosedAt == "" {
		t.Fatalf("unexpected closed project: %#v", projects)
	}
	content, err := os.ReadFile(workstream.SourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "status: closed") || !strings.Contains(string(content), "closed_at:") {
		t.Fatalf("workstream not closed:\n%s", content)
	}
	if err := store.SetProjectClosed(projects[0], []Entity{{SourcePath: workstream.SourcePath, Type: "workstream", ID: "core"}}, false); err != nil {
		t.Fatal(err)
	}
	content, err = os.ReadFile(project.SourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(content), "closed_at:") || !strings.Contains(string(content), "status: active") {
		t.Fatalf("project not reopened:\n%s", content)
	}
}
