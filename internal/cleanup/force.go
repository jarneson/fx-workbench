package cleanup

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

type ForcePlan struct {
	Repository string    `json:"repository"`
	Worktree   string    `json:"worktree"`
	Branch     string    `json:"branch,omitempty"`
	BranchTip  string    `json:"branch_tip,omitempty"`
	Sessions   []Session `json:"sessions,omitempty"`
	Changes    []string  `json:"changes,omitempty"`
	Warnings   []string  `json:"warnings,omitempty"`
	Blockers   []string  `json:"blockers,omitempty"`
	Eligible   bool      `json:"eligible"`
}

type ForceResult struct {
	Plan            ForcePlan `json:"plan"`
	SessionsKilled  int       `json:"sessions_killed"`
	WindowsKilled   int       `json:"windows_killed"`
	WorktreeRemoved bool      `json:"worktree_removed"`
	BranchDeleted   bool      `json:"branch_deleted"`
}

func (s *Service) PlanForce(ctx context.Context, repository, selectedWorktree string) ForcePlan {
	plan := ForcePlan{Worktree: canonical(selectedWorktree)}
	root, err := s.gitOutput(ctx, repository, "rev-parse", "--show-toplevel")
	if err != nil {
		plan.Blockers = append(plan.Blockers, "resolve repository: "+err.Error())
		return plan
	}
	plan.Repository = strings.TrimSpace(root)
	worktrees, err := s.worktrees(ctx, plan.Repository)
	if err != nil {
		plan.Blockers = append(plan.Blockers, err.Error())
		return plan
	}
	rootWorktree := canonical(plan.Repository)
	var selected gitWorktree
	for _, worktree := range worktrees {
		if worktree.Branch == "refs/heads/main" {
			rootWorktree = canonical(worktree.Path)
		}
		if canonical(worktree.Path) == plan.Worktree {
			selected = worktree
		}
	}
	if selected.Path == "" {
		plan.Blockers = append(plan.Blockers, "selected path is not a registered worktree")
		return plan
	}
	if plan.Worktree == rootWorktree {
		plan.Blockers = append(plan.Blockers, "refusing to force-remove the root worktree")
	}
	plan.Branch = strings.TrimPrefix(selected.Branch, "refs/heads/")
	if plan.Branch != "" {
		if tip, tipErr := s.gitOutput(ctx, plan.Repository, "rev-parse", plan.Branch); tipErr == nil {
			plan.BranchTip = strings.TrimSpace(tip)
		}
	}
	dirty, statusErr := s.worktreeStatus(ctx, plan.Worktree)
	if statusErr != nil {
		plan.Warnings = append(plan.Warnings, "could not inspect worktree status: "+statusErr.Error())
	} else {
		plan.Changes = append(plan.Changes, dirty...)
	}
	plan.Warnings = append(plan.Warnings, "forced cleanup discards all tracked and untracked changes in the selected worktree")
	sessions, sessionErr := s.Sessions(ctx)
	if sessionErr != nil {
		plan.Warnings = append(plan.Warnings, "could not inspect tmux sessions: "+sessionErr.Error())
	} else {
		currentSession, currentWindow := s.CurrentTarget(ctx)
		for _, session := range sessions {
			if canonical(session.Worktree) != plan.Worktree {
				continue
			}
			if session.ID == currentSession && (session.WindowID == "" || session.WindowID == currentWindow) {
				plan.Blockers = append(plan.Blockers, "refusing to force-clean the current tmux task target; switch task windows first")
				continue
			}
			plan.Sessions = append(plan.Sessions, session)
		}
	}
	plan.Eligible = len(plan.Blockers) == 0
	return plan
}

func (s *Service) ExecuteForce(ctx context.Context, plan ForcePlan, deleteBranch bool) (ForceResult, error) {
	result := ForceResult{Plan: plan}
	if !plan.Eligible {
		return result, fmt.Errorf("forced cleanup plan is blocked")
	}
	for _, session := range plan.Sessions {
		if err := s.KillSession(ctx, session); err != nil {
			return result, err
		}
		if session.WindowID != "" {
			result.WindowsKilled++
		} else {
			result.SessionsKilled++
		}
	}
	if output, err := exec.CommandContext(ctx, s.Git, "-C", plan.Repository, "worktree", "remove", "--force", plan.Worktree).CombinedOutput(); err != nil {
		return result, fmt.Errorf("force-remove worktree: %s", commandError(output, err))
	}
	result.WorktreeRemoved = true
	if deleteBranch && plan.Branch != "" && plan.BranchTip != "" {
		if output, err := exec.CommandContext(ctx, s.Git, "-C", plan.Repository, "update-ref", "-d", "refs/heads/"+plan.Branch, plan.BranchTip).CombinedOutput(); err != nil {
			return result, fmt.Errorf("delete local branch: %s", commandError(output, err))
		}
		result.BranchDeleted = true
	}
	return result, nil
}

func samePath(left, right string) bool {
	return filepath.Clean(canonical(left)) == filepath.Clean(canonical(right))
}
