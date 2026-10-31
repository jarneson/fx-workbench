package workbench

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSessionNameIsStableAndCollisionSafe(t *testing.T) {
	base := t.TempDir()
	repositories := []string{
		filepath.Join(base, "one", "widgets"),
		filepath.Join(base, "two", "widgets"),
	}
	for _, repository := range repositories {
		if err := mkdirAll(repository); err != nil {
			t.Fatal(err)
		}
	}
	first, err := NewIdentity(RoleRoot, repositories[0], repositories[0], "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewIdentity(RoleRoot, repositories[1], repositories[1], "")
	if err != nil {
		t.Fatal(err)
	}
	if SessionName(first) == SessionName(second) {
		t.Fatalf("colliding names: %s", SessionName(first))
	}
	if got := SessionName(first); !strings.HasPrefix(got, "root-widgets-") {
		t.Fatalf("root name = %q", got)
	}
	again, err := NewIdentity(RoleRoot, repositories[0]+"/.", repositories[0], "")
	if err != nil {
		t.Fatal(err)
	}
	if SessionName(first) != SessionName(again) || first.WorkspaceID != again.WorkspaceID {
		t.Fatalf("identity is not stable: %#v != %#v", first, again)
	}
}

func TestWorkstreamIdentityIncludesProjectWorkstreamAndCanonicalRepository(t *testing.T) {
	root := t.TempDir()
	alias := filepath.Join(t.TempDir(), "repository-link")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	first, err := NewWorkstreamIdentity("demo", "core", root)
	if err != nil {
		t.Fatal(err)
	}
	throughAlias, err := NewWorkstreamIdentity("demo", "core", alias)
	if err != nil {
		t.Fatal(err)
	}
	if first != throughAlias {
		t.Fatalf("canonical identity differs: %#v != %#v", first, throughAlias)
	}
	for _, input := range [][3]string{{"other", "core", root}, {"demo", "other", root}, {"demo", "core", t.TempDir()}} {
		other, identityErr := NewWorkstreamIdentity(input[0], input[1], input[2])
		if identityErr != nil {
			t.Fatal(identityErr)
		}
		if first.SessionName == other.SessionName {
			t.Fatalf("workstream session collision: %#v and %#v", first, other)
		}
	}
	options := first.SessionOptions()
	if len(options) != 5 || options[0] != [2]string{"@fx_workflow_role", RoleWorkstream} || options[3] != [2]string{"@fx_workstream_id", "core"} {
		t.Fatalf("session contract = %#v", options)
	}
}

func TestWorkspaceNamesDoNotCollideAcrossSameBasenameRepositories(t *testing.T) {
	base := t.TempDir()
	firstRepository := filepath.Join(base, "one", "widgets")
	secondRepository := filepath.Join(base, "two", "widgets")
	firstWorktree := filepath.Join(base, "one", "worktrees", "feature")
	secondWorktree := filepath.Join(base, "two", "worktrees", "feature")
	for _, path := range []string{firstRepository, secondRepository, firstWorktree, secondWorktree} {
		if err := mkdirAll(path); err != nil {
			t.Fatal(err)
		}
	}
	first, _ := NewIdentity(RoleWorkspace, firstRepository, firstWorktree, "")
	second, _ := NewIdentity(RoleWorkspace, secondRepository, secondWorktree, "")
	if SessionName(first) == SessionName(second) {
		t.Fatalf("same-basename repository workspaces collided at %q", SessionName(first))
	}
}

func TestTaskNamesIncludeRepositoryAndWorktreeIdentity(t *testing.T) {
	base := t.TempDir()
	repository := filepath.Join(base, "widgets")
	firstPath := filepath.Join(base, "worktrees", "one", "same-branch")
	secondPath := filepath.Join(base, "worktrees", "two", "same-branch")
	for _, path := range []string{repository, firstPath, secondPath} {
		if err := mkdirAll(path); err != nil {
			t.Fatal(err)
		}
	}
	first, _ := NewIdentity(RoleTask, repository, firstPath, "task-one")
	second, _ := NewIdentity(RoleTask, repository, secondPath, "task-two")
	if SessionName(first) == SessionName(second) {
		t.Fatalf("colliding task names: %s", SessionName(first))
	}
	if first.WorkspaceID != second.WorkspaceID {
		t.Fatalf("tasks in one repository have different workspaces: %s != %s", first.WorkspaceID, second.WorkspaceID)
	}
}

func mkdirAll(path string) error {
	return os.MkdirAll(path, 0o755)
}
