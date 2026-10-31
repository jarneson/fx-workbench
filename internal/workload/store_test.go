package workload

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestListNormalizesLegacyTask(t *testing.T) {
	vault := t.TempDir()
	path := filepath.Join(vault, "Projects", "demo", "agent-work", "tasks", "2026-09-29-ship-widget.md")
	writeTestFile(t, path, `---
type: agent-task
status: ready
project: demo
created: 2026-09-29T00:00:00Z
updated: 2026-09-29T00:00:00Z
depends_on: []
---

# Ship widget
`)
	store := &Store{Vault: vault}
	entities, err := store.List(ListOptions{Type: "task", Project: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	if len(entities) != 1 {
		t.Fatalf("got %d tasks, want 1", len(entities))
	}
	entity := entities[0]
	if entity.ID != "ship-widget" || entity.Title != "Ship widget" || !entity.Legacy {
		t.Fatalf("unexpected legacy normalization: %#v", entity)
	}
	if !strings.Contains(strings.Join(entity.Diagnostics, ";"), "stable id derived from filename") {
		t.Fatalf("expected stable-id diagnostic, got %v", entity.Diagnostics)
	}
}

func TestFindPrefersSelectedProjectOverSameIDWorkstream(t *testing.T) {
	vault := t.TempDir()
	store := &Store{Vault: vault}
	_, err := store.Scaffold(Entity{
		Type: "project", ID: "demo", Title: "Demo", Status: "active", RootRepository: "/tmp/demo",
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Scaffold(Entity{
		Type: "workstream", ID: "demo", Title: "Demo delivery", Status: "active", Project: "demo",
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("FX_WORKBENCH_PROJECT", "demo")
	entity, err := store.Find("demo", "")
	if err != nil {
		t.Fatal(err)
	}
	if entity.Type != "project" {
		t.Fatalf("got %s, want project", entity.Type)
	}
	workstream, err := store.FindType("demo", "demo", "workstream")
	if err != nil {
		t.Fatal(err)
	}
	if workstream.Type != "workstream" {
		t.Fatalf("got %s, want workstream", workstream.Type)
	}
}

func TestListAllProjectsIgnoresCurrentProjectSelection(t *testing.T) {
	store := &Store{Vault: t.TempDir()}
	for _, project := range []string{"alpha", "beta"} {
		if _, err := store.Scaffold(Entity{Type: "project", ID: project, Title: strings.ToUpper(project), Status: "active", RootRepository: "/tmp/" + project}, ""); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Scaffold(Entity{Type: "workstream", ID: project + "-stream", Title: project + " stream", Status: "active", Project: project}, ""); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("FX_WORKBENCH_PROJECT", "alpha")
	entities, err := store.List(ListOptions{Type: "workstream", AllProjects: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(entities) != 2 || entities[0].Project != "alpha" || entities[1].Project != "beta" {
		t.Fatalf("all-project workstreams = %#v", entities)
	}
}

func TestScaffoldProjectRegistersRootRepository(t *testing.T) {
	store := &Store{Vault: t.TempDir()}
	entity, err := store.Scaffold(Entity{
		Type: "project", ID: "demo", Title: "Demo", Status: "active",
		RootRepository: "/tmp/root", Repositories: []string{"/tmp/other"},
	}, "Coordinate demo work.")
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := parseFile(entity.SourcePath, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.RootRepository != "/tmp/root" || len(loaded.Repositories) != 2 || loaded.Repositories[0] != "/tmp/root" {
		t.Fatalf("unexpected repository registry: %#v", loaded)
	}
}

func TestScaffoldTaskAndUpdateStatusPreservesBody(t *testing.T) {
	vault := t.TempDir()
	store := &Store{Vault: vault}
	if _, err := store.Scaffold(Entity{
		Type: "project", ID: "demo", Title: "Demo", Status: "active", RootRepository: "/tmp/repo",
	}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Scaffold(Entity{
		Type: "workstream", ID: "widgets", Title: "Widgets", Status: "active", Project: "demo",
	}, ""); err != nil {
		t.Fatal(err)
	}
	entity, err := store.Scaffold(Entity{
		Type: "task", ID: "ship-widget", Title: "Ship widget", Status: "ready",
		Project: "demo", Workstream: "widgets", Repository: "/tmp/repo", Branch: "ship-widget",
	}, "Deliver the widget.")
	if err != nil {
		t.Fatal(err)
	}
	if got := filepath.Base(entity.SourcePath); got != "01-ship-widget.md" {
		t.Fatalf("task filename = %q, want 01-ship-widget.md", got)
	}
	second, err := store.Scaffold(Entity{
		Type: "task", ID: "polish-widget", Title: "Polish widget", Status: "draft",
		Project: "demo", Workstream: "widgets", Repository: "/tmp/repo",
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := filepath.Base(second.SourcePath); got != "02-polish-widget.md" {
		t.Fatalf("second task filename = %q, want 02-polish-widget.md", got)
	}
	content, err := os.ReadFile(entity.SourcePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"schema: fx-workbench/v1", "id: ship-widget", "workstream: widgets", "# Ship widget", "Deliver the widget."} {
		if !strings.Contains(string(content), expected) {
			t.Fatalf("scaffold missing %q:\n%s", expected, content)
		}
	}
	if err := store.SetStatus(entity, "active"); err != nil {
		t.Fatal(err)
	}
	updated, err := os.ReadFile(entity.SourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(updated), "status: active") || !strings.Contains(string(updated), "Deliver the widget.") {
		t.Fatalf("status update did not preserve body:\n%s", updated)
	}
}

func TestScaffoldRejectsIncompleteTask(t *testing.T) {
	store := &Store{Vault: t.TempDir()}
	_, err := store.Scaffold(Entity{Type: "task", ID: "ship-widget", Title: "Ship widget"}, "")
	if err == nil || !strings.Contains(err.Error(), "project, workstream, and repository") {
		t.Fatalf("got error %v", err)
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
