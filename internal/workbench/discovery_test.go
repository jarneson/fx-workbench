package workbench

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestLocalDiscoveryListsRepositoriesAndWorktreesWithWhitespace(t *testing.T) {
	home := t.TempDir()
	dev := filepath.Join(home, "dev")
	repository := filepath.Join(dev, "repo with spaces")
	worktreeRoot := filepath.Join(dev, "worktrees", "repo with spaces")
	worktree := filepath.Join(worktreeRoot, "feature branch")
	if err := os.MkdirAll(repository, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, repository, "init", "-q")
	git(t, repository, "config", "user.email", "test@example.com")
	git(t, repository, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repository, "README"), []byte("test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repository, "add", "README")
	git(t, repository, "commit", "-qm", "initial")
	if err := os.MkdirAll(worktreeRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, repository, "worktree", "add", "-q", "-b", "feature-branch", worktree)
	if err := os.MkdirAll(filepath.Join(dev, "not a repository"), 0o755); err != nil {
		t.Fatal(err)
	}

	discovery := NewLocalDiscovery(DiscoveryPolicy{
		RepositoryDirectories: []string{dev},
		WorktreeDirectories:   []string{filepath.Join(dev, "worktrees")},
		ExcludedNames:         []string{"worktrees"},
	})
	workspaces, err := discovery.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	repository, _ = canonicalPath(repository)
	worktree, _ = canonicalPath(worktree)
	if len(workspaces) != 2 {
		t.Fatalf("workspaces = %#v, want repository and worktree", workspaces)
	}
	if workspaces[0].Kind != WorkspaceRepository || workspaces[0].Path != repository || workspaces[0].Label != "repo with spaces" {
		t.Fatalf("repository = %#v", workspaces[0])
	}
	if workspaces[1].Kind != WorkspaceWorktree || workspaces[1].Path != worktree || workspaces[1].Repository != repository || workspaces[1].Label != "repo with spaces/feature branch" {
		t.Fatalf("worktree = %#v", workspaces[1])
	}
}

func git(t *testing.T, directory string, args ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, args...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}
