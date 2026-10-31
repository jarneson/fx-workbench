package workload

import (
	"os/exec"
	"strings"
	"testing"
)

func TestResolveProjectUsesSelectedProjectToBreakSharedRepositoryTie(t *testing.T) {
	vault := t.TempDir()
	repository := initTestRepository(t)
	store := &Store{Vault: vault}
	for _, id := range []string{"alpha", "beta"} {
		if _, err := store.Scaffold(Entity{
			Type: "project", ID: id, Title: id, Status: "active",
			RootRepository: repository, Repositories: []string{repository},
		}, ""); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("FX_WORKBENCH_PROJECT", "beta")
	resolution, err := store.ResolveProject(repository, "")
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Project.ID != "beta" || resolution.Reason != "root-repository-path" {
		t.Fatalf("unexpected resolution: %#v", resolution)
	}
}

func TestResolveProjectPrefersRootRepositoryOverSelectedSecondaryRegistration(t *testing.T) {
	vault := t.TempDir()
	workspace := initTestRepository(t)
	otherRoot := initTestRepository(t)
	store := &Store{Vault: vault}
	if _, err := store.Scaffold(Entity{
		Type: "project", ID: "owner", Title: "Owner", Status: "active",
		RootRepository: workspace, Repositories: []string{workspace},
	}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Scaffold(Entity{
		Type: "project", ID: "secondary", Title: "Secondary", Status: "active",
		RootRepository: otherRoot, Repositories: []string{otherRoot, workspace},
	}, ""); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FX_WORKBENCH_PROJECT", "secondary")
	resolution, err := store.ResolveProject(workspace, "")
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Project.ID != "owner" || resolution.Reason != "root-repository-path" {
		t.Fatalf("unexpected resolution: %#v", resolution)
	}
}

func TestResolveProjectMatchesWorktreeStyleCheckoutByRemote(t *testing.T) {
	vault := t.TempDir()
	registered := initTestRepository(t)
	workspace := initTestRepository(t)
	for _, repository := range []string{registered, workspace} {
		if output, err := exec.Command("git", "-C", repository, "remote", "add", "origin", "git@github.com:example/demo.git").CombinedOutput(); err != nil {
			t.Fatalf("add remote: %v: %s", err, output)
		}
	}
	store := &Store{Vault: vault}
	if _, err := store.Scaffold(Entity{
		Type: "project", ID: "demo", Title: "Demo", Status: "active",
		RootRepository: registered, Repositories: []string{registered},
	}, ""); err != nil {
		t.Fatal(err)
	}
	resolution, err := store.ResolveProject(workspace, "")
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Project.ID != "demo" || resolution.Reason != "root-repository-remote" {
		t.Fatalf("unexpected resolution: %#v", resolution)
	}
}

func TestResolveProjectRejectsUnregisteredGitWorkspace(t *testing.T) {
	vault := t.TempDir()
	registered := initTestRepository(t)
	workspace := initTestRepository(t)
	store := &Store{Vault: vault}
	if _, err := store.Scaffold(Entity{
		Type: "project", ID: "demo", Title: "Demo", Status: "active",
		RootRepository: registered, Repositories: []string{registered},
	}, ""); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FX_WORKBENCH_PROJECT", "demo")
	_, err := store.ResolveProject(workspace, "")
	if err == nil || !strings.Contains(err.Error(), "no project registers") {
		t.Fatalf("got error %v", err)
	}
}
