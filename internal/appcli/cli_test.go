package appcli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	runtimeworkbench "github.com/jarneson/fx-workbench/internal/workbench"
	"github.com/jarneson/fx-workbench/internal/workload"
)

func TestPlanDryRunDoesNotRequireTmux(t *testing.T) {
	vault := t.TempDir()
	root := t.TempDir()
	t.Setenv("OBSIDIAN_VAULT", vault)
	t.Setenv("FX_WORKBENCH_PROJECT", "demo")
	store := &workload.Store{Vault: vault}
	if _, err := store.Scaffold(workload.Entity{
		Type: "project", ID: "demo", Title: "Demo", Status: "active", RootRepository: root,
	}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Scaffold(workload.Entity{
		Type: "workstream", ID: "core", Title: "Core", Status: "active", Project: "demo", Repository: root,
	}, ""); err != nil {
		t.Fatal(err)
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := runtimeworkbench.NewWorkstreamIdentity("demo", "core", canonicalRoot)
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := Run([]string{"plan", "--project", "demo", "--workstream", "core", "--dry-run", "--tmux-bin", "/does/not/exist"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, stderr.String())
	}
	for _, expected := range []string{"project: demo", "workstream: core", "target repository: " + canonicalRoot, "session target: " + identity.SessionName, "dry-run: planner preflight passed", "No planning action is requested by this bootstrap message", "ask the user what they would like to plan together", "Planning completion alone is not approval", "explicitly approves a proposed launch", "worktree-task-launcher skill", "obsidian-planner skill"} {
		if !strings.Contains(stdout.String(), expected) {
			t.Errorf("output missing %q:\n%s", expected, stdout.String())
		}
	}
}

func TestStatusClosesAndReopensEligibleWorkstream(t *testing.T) {
	vault := t.TempDir()
	t.Setenv("OBSIDIAN_VAULT", vault)
	t.Setenv("FX_WORKBENCH_PROJECT", "demo")
	store := &workload.Store{Vault: vault}
	if _, err := store.Scaffold(workload.Entity{Type: "project", ID: "demo", Title: "Demo", Status: "active", RootRepository: "/tmp/demo"}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Scaffold(workload.Entity{Type: "workstream", ID: "core", Title: "Core", Status: "active", Project: "demo"}, ""); err != nil {
		t.Fatal(err)
	}
	task, err := store.Scaffold(workload.Entity{Type: "task", ID: "ship", Title: "Ship", Status: "done", Project: "demo", Workstream: "core", Repository: "/tmp/demo"}, "")
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"status", "--project", "demo", "--set", "closed", "core"}, &stdout, &stderr); code == 0 || !strings.Contains(stderr.String(), "done=1") {
		t.Fatalf("nonterminal close code=%d stderr=%s", code, stderr.String())
	}
	if err := store.SetStatus(task, "merged"); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"status", "--project", "demo", "--set", "closed", "core"}, &stdout, &stderr); code != 0 {
		t.Fatalf("close code=%d stderr=%s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"status", "--project", "demo", "--set", "active", "core"}, &stdout, &stderr); code != 0 {
		t.Fatalf("reopen code=%d stderr=%s", code, stderr.String())
	}
}

func TestStatusActivatesTaskWithFXSessionID(t *testing.T) {
	vault := t.TempDir()
	t.Setenv("OBSIDIAN_VAULT", vault)
	t.Setenv("FX_WORKBENCH_PROJECT", "demo")
	store := &workload.Store{Vault: vault}
	if _, err := store.Scaffold(workload.Entity{Type: "project", ID: "demo", Title: "Demo", Status: "active", RootRepository: "/tmp/demo"}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Scaffold(workload.Entity{Type: "workstream", ID: "core", Title: "Core", Status: "active", Project: "demo"}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Scaffold(workload.Entity{Type: "task", ID: "ship", Title: "Ship", Status: "ready", Project: "demo", Workstream: "core", Repository: "/tmp/demo"}, ""); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := Run([]string{"status", "--project", "demo", "--set", "coding", "--fx-session-id", "fx-task-123", "ship"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, stderr.String())
	}
	loaded, err := store.Find("ship", "demo")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != "coding" || loaded.FXSessionID != "fx-task-123" {
		t.Fatalf("launched task = %#v", loaded)
	}
}

func TestShowAcceptsJSONAfterReference(t *testing.T) {
	vault := t.TempDir()
	t.Setenv("OBSIDIAN_VAULT", vault)
	t.Setenv("FX_WORKBENCH_PROJECT", "demo")
	store := &workload.Store{Vault: vault}
	if _, err := store.Scaffold(workload.Entity{
		Type: "project", ID: "demo", Title: "Demo", Status: "active", RootRepository: "/tmp/demo",
	}, ""); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"show", "demo", "--json"}, &stdout, &stderr); code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"id": "demo"`) {
		t.Fatalf("unexpected output: %s", stdout.String())
	}
}
