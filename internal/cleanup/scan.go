package cleanup

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/jarneson/fx-workbench/internal/workload"
)

const (
	CategorySafe    = "safe"
	CategoryReview  = "review"
	CategoryBlocked = "blocked"
)

type WorktreePlan struct {
	Path     string `json:"path"`
	Branch   string `json:"branch"`
	TaskID   string `json:"task_id,omitempty"`
	Category string `json:"category"`
	Plan     Plan   `json:"plan"`
}

func (s *Service) ScanWorktrees(ctx context.Context, repository string, tasks []workload.Entity) ([]WorktreePlan, error) {
	root, err := s.gitOutput(ctx, repository, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("resolve repository: %w", err)
	}
	root = strings.TrimSpace(root)
	worktrees, err := s.worktrees(ctx, root)
	if err != nil {
		return nil, err
	}
	rootWorktree := root
	for _, worktree := range worktrees {
		if worktree.Branch == "refs/heads/main" {
			rootWorktree = worktree.Path
			break
		}
	}
	tasksByBranch := make(map[string][]workload.Entity)
	for _, task := range tasks {
		if task.Repository != "" && canonical(task.Repository) == canonical(rootWorktree) && task.Branch != "" {
			tasksByBranch[task.Branch] = append(tasksByBranch[task.Branch], task)
		}
	}
	result := make([]WorktreePlan, 0)
	for _, worktree := range worktrees {
		if canonical(worktree.Path) == canonical(rootWorktree) {
			continue
		}
		branch := strings.TrimPrefix(worktree.Branch, "refs/heads/")
		entry := WorktreePlan{Path: worktree.Path, Branch: branch, Category: CategoryBlocked}
		matches := tasksByBranch[branch]
		switch len(matches) {
		case 1:
			entry.TaskID = matches[0].ID
			entry.Plan = s.Plan(ctx, matches[0])
			if entry.Plan.Eligible {
				entry.Category = CategorySafe
			} else {
				trusted := s.PlanWithOptions(ctx, matches[0], PlanOptions{TrustMerged: true})
				if trusted.Eligible && trusted.TrustedMerged {
					entry.Category = CategoryReview
				}
			}
		case 0:
			entry.Plan = Plan{
				Task:     workload.Entity{ID: branch, Branch: branch, Repository: rootWorktree},
				Worktree: worktree.Path, Branch: branch,
				Blockers: []string{"no canonical task maps to this worktree branch"},
			}
		default:
			entry.Plan = Plan{
				Task: matches[0], Worktree: worktree.Path, Branch: branch,
				Blockers: []string{"multiple canonical tasks map to this worktree branch"},
			}
		}
		result = append(result, entry)
	}
	sort.SliceStable(result, func(i, j int) bool {
		rank := map[string]int{CategorySafe: 0, CategoryReview: 1, CategoryBlocked: 2}
		if rank[result[i].Category] != rank[result[j].Category] {
			return rank[result[i].Category] < rank[result[j].Category]
		}
		return result[i].Branch < result[j].Branch
	})
	return result, nil
}
