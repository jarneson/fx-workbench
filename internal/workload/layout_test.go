package workload

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOrderTasksHonorsDependenciesBeforeStableTieBreakers(t *testing.T) {
	tasks := []Entity{
		{ID: "launch", Created: "2026-09-29T00:00:00Z", DependsOn: []string{"workspace"}},
		{ID: "workspace", Created: "2026-09-29T00:00:00Z"},
	}
	ordered := orderTasks(tasks)
	if ordered[0].ID != "workspace" || ordered[1].ID != "launch" {
		t.Fatalf("unexpected order: %#v", ordered)
	}
}

func TestMigrateLayoutMovesCanonicalHierarchyAndRemovesEmptyAgentWork(t *testing.T) {
	vault := t.TempDir()
	store := &Store{Vault: vault}
	legacy := filepath.Join(vault, "Projects", "demo", "agent-work")
	writeTestFile(t, filepath.Join(legacy, "project.md"), canonicalProject("demo", "/tmp/demo"))
	writeTestFile(t, filepath.Join(legacy, "workstreams", "2026-09-29-tooling.md"), canonicalWorkstream("demo", "tooling"))
	writeTestFile(t, filepath.Join(legacy, "tasks", "2026-09-29-tooling-second.md"), canonicalOrderedTask("demo", "tooling", "tooling-second", "2026-09-29T02:00:00Z"))
	writeTestFile(t, filepath.Join(legacy, "tasks", "2026-09-29-tooling-first.md"), canonicalOrderedTask("demo", "tooling", "tooling-first", "2026-09-29T01:00:00Z"))

	plan, err := store.MigrateLayout("demo", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Moves) != 4 {
		t.Fatalf("got %d moves, want 4: %#v", len(plan.Moves), plan.Moves)
	}
	if _, err := os.Stat(filepath.Join(vault, "Projects", "demo", "project.md")); !os.IsNotExist(err) {
		t.Fatalf("dry-run wrote canonical manifest: %v", err)
	}

	result, err := store.MigrateLayout("demo", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Moves) != 4 {
		t.Fatalf("got %d applied moves, want 4", len(result.Moves))
	}
	for _, relative := range []string{
		"project.md",
		"01-tooling/workstream.md",
		"01-tooling/01-first.md",
		"01-tooling/02-second.md",
	} {
		if _, err := os.Stat(filepath.Join(vault, "Projects", "demo", relative)); err != nil {
			t.Fatalf("missing %s: %v", relative, err)
		}
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("legacy agent-work still exists: %v", err)
	}
	entities, err := store.List(ListOptions{Project: "demo", Type: "task"})
	if err != nil {
		t.Fatal(err)
	}
	if len(entities) != 2 || entities[0].Order != 1 || entities[1].Order != 2 {
		t.Fatalf("unexpected migrated tasks: %#v", entities)
	}
}

func canonicalProject(id, root string) string {
	return "---\n" +
		"schema: fx-workbench/v1\n" +
		"type: project\n" +
		"id: " + id + "\n" +
		"title: Demo\n" +
		"status: active\n" +
		"project: " + id + "\n" +
		"root_repository: " + root + "\n" +
		"repositories:\n  - " + root + "\n" +
		"created: 2026-09-29T00:00:00Z\n" +
		"updated: 2026-09-29T00:00:00Z\n" +
		"---\n\n# Demo\n"
}

func canonicalOrderedTask(project, workstream, id, created string) string {
	return "---\n" +
		"schema: fx-workbench/v1\n" +
		"type: task\n" +
		"id: " + id + "\n" +
		"title: " + id + "\n" +
		"status: ready\n" +
		"project: " + project + "\n" +
		"workstream: " + workstream + "\n" +
		"repository: /tmp/demo\n" +
		"created: " + created + "\n" +
		"updated: " + created + "\n" +
		"---\n\n# " + id + "\n"
}
