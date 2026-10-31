package workload

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestOnboardDryRunDoesNotWrite(t *testing.T) {
	vault := t.TempDir()
	repository := initTestRepository(t)
	store := &Store{Vault: vault}
	result, err := store.Onboard(OnboardOptions{
		ID: "demo", Title: "Demo", RootRepository: repository,
		WorkstreamID: "core", WorkstreamTitle: "Core", DryRun: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.DryRun || result.Project.ID != "demo" || result.Workstream.ID != "core" {
		t.Fatalf("unexpected result: %#v", result)
	}
	if _, err := os.Stat(filepath.Join(vault, "Projects", "demo")); !os.IsNotExist(err) {
		t.Fatalf("dry-run wrote project directory: %v", err)
	}
}

func TestOnboardCreatesProjectAndWorkstream(t *testing.T) {
	vault := t.TempDir()
	root := initTestRepository(t)
	other := initTestRepository(t)
	store := &Store{Vault: vault}
	result, err := store.Onboard(OnboardOptions{
		ID: "demo", Title: "Demo", Objective: "Coordinate demo work.",
		RootRepository: root, Repositories: []string{other},
		WorkstreamID: "delivery", WorkstreamTitle: "Delivery", Select: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	actualRoot, err := filepath.EvalSymlinks(result.Repositories[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Repositories) != 2 || actualRoot != canonicalRoot {
		t.Fatalf("unexpected repositories: %v", result.Repositories)
	}
	for _, path := range []string{result.Project.SourcePath, result.Workstream.SourcePath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("missing onboarded file %s: %v", path, err)
		}
	}
}

func TestOnboardMigratesCanonicalWorkstream(t *testing.T) {
	vault := t.TempDir()
	root := initTestRepository(t)
	store := &Store{Vault: vault}
	sourceAgent := filepath.Join(vault, "Projects", "old", "agent-work")
	workstreamPath := filepath.Join(sourceAgent, "2026-09-29-tooling.md")
	taskPath := filepath.Join(sourceAgent, "tasks", "2026-09-29-ship-cli.md")
	writeTestFile(t, workstreamPath, canonicalWorkstream("old", "tooling"))
	writeTestFile(t, taskPath, canonicalTask("old", "tooling", root))

	result, err := store.Onboard(OnboardOptions{
		ID: "new", Title: "New", RootRepository: root,
		FromProject: "old", MigrateWorkstream: "tooling", Select: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Migrations) != 2 {
		t.Fatalf("got %d migrations, want 2", len(result.Migrations))
	}
	for _, migration := range result.Migrations {
		content, err := os.ReadFile(migration.Destination)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(content), "project: new") {
			t.Fatalf("destination not reparented:\n%s", content)
		}
		redirect, err := os.ReadFile(migration.SourcePath)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(string(redirect), "# Moved:") {
			t.Fatalf("source is not a redirect:\n%s", redirect)
		}
	}
}

func TestOnboardRejectsRepositorySubdirectory(t *testing.T) {
	root := initTestRepository(t)
	nested := filepath.Join(root, "nested")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	store := &Store{Vault: t.TempDir()}
	_, err := store.Onboard(OnboardOptions{ID: "demo", Title: "Demo", RootRepository: nested, DryRun: true})
	if err == nil || !strings.Contains(err.Error(), "pass the repository root") {
		t.Fatalf("got error %v", err)
	}
}

func initTestRepository(t *testing.T) string {
	t.Helper()
	path := t.TempDir()
	if output, err := exec.Command("git", "init", "-q", path).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	return path
}

func canonicalWorkstream(project, id string) string {
	return "---\n" +
		"schema: fx-workbench/v1\n" +
		"type: workstream\n" +
		"id: " + id + "\n" +
		"title: Tooling\n" +
		"status: active\n" +
		"project: " + project + "\n" +
		"created: 2026-09-29T00:00:00Z\n" +
		"updated: 2026-09-29T00:00:00Z\n" +
		"---\n\n# Tooling\n"
}

func canonicalTask(project, workstream, repository string) string {
	return "---\n" +
		"schema: fx-workbench/v1\n" +
		"type: task\n" +
		"id: ship-cli\n" +
		"title: Ship CLI\n" +
		"status: ready\n" +
		"project: " + project + "\n" +
		"workstream: " + workstream + "\n" +
		"repository: " + repository + "\n" +
		"created: 2026-09-29T00:00:00Z\n" +
		"updated: 2026-09-29T00:00:00Z\n" +
		"---\n\n# Ship CLI\n"
}
