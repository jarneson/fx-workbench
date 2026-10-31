package workbench

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

type WorkspaceKind string

const (
	WorkspaceRepository WorkspaceKind = "repository"
	WorkspaceWorktree   WorkspaceKind = "worktree"
)

type Workspace struct {
	Kind       WorkspaceKind
	Label      string
	Path       string
	Repository string
}

type WorkspaceDiscovery interface {
	Discover(context.Context) ([]Workspace, error)
}

type WorkspaceLauncher interface {
	OpenWorkspace(context.Context, Workspace) (OpenResult, error)
}

type DiscoveryPolicy struct {
	Repositories          []string
	RepositoryDirectories []string
	WorktreeDirectories   []string
	ExcludedNames         []string
}

type LocalDiscovery struct {
	policy DiscoveryPolicy
	git    string
}

func DefaultDiscoveryPolicy(home string) DiscoveryPolicy {
	return DiscoveryPolicy{
		RepositoryDirectories: []string{filepath.Join(home, "dev")},
		WorktreeDirectories:   []string{filepath.Join(home, "dev", "worktrees")},
		ExcludedNames:         []string{"worktrees"},
	}
}

func NewLocalDiscovery(policy DiscoveryPolicy) *LocalDiscovery {
	return &LocalDiscovery{policy: policy, git: "git"}
}

func (d *LocalDiscovery) Discover(ctx context.Context) ([]Workspace, error) {
	type candidate struct {
		path string
		kind WorkspaceKind
	}
	candidates := make([]candidate, 0, len(d.policy.Repositories))
	for _, path := range d.policy.Repositories {
		candidates = append(candidates, candidate{path: path, kind: WorkspaceRepository})
	}
	for _, parent := range d.policy.RepositoryDirectories {
		entries, err := os.ReadDir(parent)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("read repository directory %s: %w", parent, err)
		}
		for _, entry := range entries {
			if entry.IsDir() && !slices.Contains(d.policy.ExcludedNames, entry.Name()) {
				candidates = append(candidates, candidate{path: filepath.Join(parent, entry.Name()), kind: WorkspaceRepository})
			}
		}
	}
	for _, parent := range d.policy.WorktreeDirectories {
		repositories, err := os.ReadDir(parent)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("read worktree directory %s: %w", parent, err)
		}
		for _, repository := range repositories {
			if !repository.IsDir() {
				continue
			}
			root := filepath.Join(parent, repository.Name())
			worktrees, err := os.ReadDir(root)
			if err != nil {
				return nil, fmt.Errorf("read worktree directory %s: %w", root, err)
			}
			for _, worktree := range worktrees {
				if worktree.IsDir() {
					candidates = append(candidates, candidate{path: filepath.Join(root, worktree.Name()), kind: WorkspaceWorktree})
				}
			}
		}
	}

	seen := make(map[string]bool)
	var workspaces []Workspace
	for _, candidate := range candidates {
		workspace, err := d.inspect(ctx, candidate.path, candidate.kind)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue // Discovery roots may contain ordinary directories.
		}
		if seen[workspace.Path] {
			continue
		}
		seen[workspace.Path] = true
		workspaces = append(workspaces, workspace)
	}
	slices.SortFunc(workspaces, func(a, b Workspace) int {
		if a.Kind != b.Kind {
			if a.Kind == WorkspaceRepository {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Label, b.Label)
	})
	return workspaces, nil
}

func (d *LocalDiscovery) inspect(ctx context.Context, path string, kind WorkspaceKind) (Workspace, error) {
	path, err := canonicalPath(path)
	if err != nil {
		return Workspace{}, err
	}
	top, err := exec.CommandContext(ctx, d.git, "-C", path, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return Workspace{}, err
	}
	worktree, err := canonicalPath(strings.TrimSpace(string(top)))
	if err != nil {
		return Workspace{}, err
	}
	common, err := exec.CommandContext(ctx, d.git, "-C", path, "rev-parse", "--path-format=absolute", "--git-common-dir").Output()
	if err != nil {
		return Workspace{}, err
	}
	commonPath := strings.TrimSpace(string(common))
	repository := worktree
	if filepath.Base(commonPath) == ".git" {
		repository, err = canonicalPath(filepath.Dir(commonPath))
		if err != nil {
			return Workspace{}, err
		}
	}
	label := filepath.Base(worktree)
	if kind == WorkspaceWorktree {
		label = filepath.Base(repository) + "/" + label
	}
	return Workspace{Kind: kind, Label: label, Path: worktree, Repository: repository}, nil
}
