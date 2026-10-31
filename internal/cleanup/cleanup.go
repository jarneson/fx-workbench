package cleanup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/jarneson/fx-workbench/internal/workload"
)

type Session struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	WindowID    string `json:"window_id,omitempty"`
	WindowName  string `json:"window_name,omitempty"`
	TaskID      string `json:"task_id"`
	Worktree    string `json:"worktree"`
	Repository  string `json:"repository"`
	WorkspaceID string `json:"workspace_id"`
	Role        string `json:"role"`
}

type Plan struct {
	Task          workload.Entity `json:"task"`
	Worktree      string          `json:"worktree,omitempty"`
	Branch        string          `json:"branch,omitempty"`
	BranchTip     string          `json:"branch_tip,omitempty"`
	Sessions      []Session       `json:"sessions,omitempty"`
	Blockers      []string        `json:"blockers,omitempty"`
	Eligible      bool            `json:"eligible"`
	TrustedMerged bool            `json:"trusted_merged,omitempty"`
}

type PlanOptions struct {
	TrustMerged bool
}

type Result struct {
	Plan            Plan `json:"plan"`
	SessionsKilled  int  `json:"sessions_killed"`
	WindowsKilled   int  `json:"windows_killed"`
	WorktreeRemoved bool `json:"worktree_removed"`
	BranchDeleted   bool `json:"branch_deleted"`
}

type Service struct {
	Git  string
	Tmux string
}

func NewService() *Service {
	git := os.Getenv("FX_WORKBENCH_GIT_BIN")
	if git == "" {
		git = "git"
	}
	tmux := os.Getenv("FX_WORKBENCH_TMUX_BIN")
	if tmux == "" {
		tmux = "tmux"
	}
	return &Service{Git: git, Tmux: tmux}
}

func (s *Service) Plan(ctx context.Context, task workload.Entity) Plan {
	return s.PlanWithOptions(ctx, task, PlanOptions{})
}

func (s *Service) PlanWithOptions(ctx context.Context, task workload.Entity, options PlanOptions) Plan {
	plan := Plan{Task: task, Branch: task.Branch}
	if task.Status != "merged" {
		plan.Blockers = append(plan.Blockers, fmt.Sprintf("task status is %q, not merged", task.Status))
	}
	if task.Repository == "" || task.Branch == "" {
		plan.Blockers = append(plan.Blockers, "task repository and branch are required")
		return plan
	}
	root, err := s.gitOutput(ctx, task.Repository, "rev-parse", "--show-toplevel")
	if err != nil {
		plan.Blockers = append(plan.Blockers, "repository is not a Git checkout: "+err.Error())
		return plan
	}
	root = strings.TrimSpace(root)
	if tip, tipErr := s.gitOutput(ctx, root, "rev-parse", task.Branch); tipErr == nil {
		plan.BranchTip = strings.TrimSpace(tip)
	} else {
		plan.Blockers = append(plan.Blockers, "resolve task branch: "+tipErr.Error())
	}
	worktrees, err := s.worktrees(ctx, root)
	if err != nil {
		plan.Blockers = append(plan.Blockers, err.Error())
		return plan
	}
	branchRef := "refs/heads/" + task.Branch
	for _, worktree := range worktrees {
		if worktree.Branch == branchRef {
			if plan.Worktree != "" {
				plan.Blockers = append(plan.Blockers, "multiple worktrees use task branch")
			}
			plan.Worktree = worktree.Path
		}
	}
	if plan.Worktree == "" {
		plan.Blockers = append(plan.Blockers, "task branch has no registered worktree")
		return plan
	}
	if canonical(plan.Worktree) == canonical(root) {
		plan.Blockers = append(plan.Blockers, "refusing to remove the repository root worktree")
	}
	trustIntegration := false
	if options.TrustMerged && task.Status == "merged" {
		if s.refExists(ctx, root, "refs/remotes/origin/main") && s.refsEqual(ctx, root, "main", "origin/main") {
			trustIntegration = true
			plan.TrustedMerged = true
		} else {
			plan.Blockers = append(plan.Blockers, "--trust-merged requires synchronized local and origin main")
		}
	}
	if ok, reason := s.integrated(ctx, root, task.Branch, "main"); !ok && !trustIntegration {
		plan.Blockers = append(plan.Blockers, "branch is not integrated into main: "+reason)
	}
	if s.refExists(ctx, root, "refs/remotes/origin/main") {
		if ok, reason := s.integrated(ctx, root, task.Branch, "origin/main"); !ok && !trustIntegration {
			plan.Blockers = append(plan.Blockers, "branch is not integrated into origin/main: "+reason)
		}
	}
	dirty, err := s.worktreeStatus(ctx, plan.Worktree)
	if err != nil {
		plan.Blockers = append(plan.Blockers, err.Error())
	} else if len(dirty) > 0 {
		plan.Blockers = append(plan.Blockers, "worktree has user changes: "+strings.Join(dirty, ", "))
	}
	sessions, err := s.taskSessions(ctx, task.ID)
	if err != nil {
		plan.Blockers = append(plan.Blockers, err.Error())
	} else {
		currentSession, currentWindow := s.CurrentTarget(ctx)
		for _, session := range sessions {
			if session.ID == currentSession && (session.WindowID == "" || session.WindowID == currentWindow) {
				plan.Blockers = append(plan.Blockers, "refusing to clean the current tmux task target; switch to another task window first")
				continue
			}
			validRole := session.Role == "task" || (session.Role == "workstream" && session.WindowID != "")
			if canonical(session.Worktree) != canonical(plan.Worktree) || !validRole {
				plan.Blockers = append(plan.Blockers, fmt.Sprintf("tmux target %s identity does not match task worktree", session.Name))
				continue
			}
			plan.Sessions = append(plan.Sessions, session)
		}
	}
	plan.Eligible = len(plan.Blockers) == 0
	return plan
}

func (s *Service) Execute(ctx context.Context, plan Plan, deleteBranch bool) (Result, error) {
	result := Result{Plan: plan}
	if !plan.Eligible {
		return result, errors.New("cleanup plan is blocked")
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
	root := plan.Task.Repository
	if output, err := exec.CommandContext(ctx, s.Git, "-C", root, "worktree", "remove", plan.Worktree).CombinedOutput(); err != nil {
		return result, fmt.Errorf("remove worktree: %s", commandError(output, err))
	}
	result.WorktreeRemoved = true
	if deleteBranch {
		if output, err := exec.CommandContext(ctx, s.Git, "-C", root, "branch", "-d", plan.Branch).CombinedOutput(); err != nil {
			// Cherry-picked branches are patch-equivalent but not ancestors. The plan
			// already proved integration; update-ref also protects against a branch
			// moving after the plan by requiring the exact observed old value.
			if plan.BranchTip == "" {
				return result, fmt.Errorf("delete local branch: %s", commandError(output, err))
			}
			if refOutput, refErr := exec.CommandContext(ctx, s.Git, "-C", root, "update-ref", "-d", "refs/heads/"+plan.Branch, plan.BranchTip).CombinedOutput(); refErr != nil {
				return result, fmt.Errorf("delete patch-equivalent branch: %s", commandError(refOutput, refErr))
			}
		}
		result.BranchDeleted = true
	}
	return result, nil
}

type gitWorktree struct {
	Path   string
	Branch string
}

func (s *Service) worktrees(ctx context.Context, root string) ([]gitWorktree, error) {
	output, err := s.gitOutput(ctx, root, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, fmt.Errorf("list worktrees: %w", err)
	}
	var result []gitWorktree
	var current gitWorktree
	for _, line := range strings.Split(output, "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			if current.Path != "" {
				result = append(result, current)
			}
			current = gitWorktree{Path: strings.TrimPrefix(line, "worktree ")}
		case strings.HasPrefix(line, "branch "):
			current.Branch = strings.TrimPrefix(line, "branch ")
		}
	}
	if current.Path != "" {
		result = append(result, current)
	}
	return result, nil
}

func (s *Service) integrated(ctx context.Context, root, branch, target string) (bool, string) {
	if err := exec.CommandContext(ctx, s.Git, "-C", root, "merge-base", "--is-ancestor", branch, target).Run(); err == nil {
		return true, "ancestor"
	}
	output, err := s.gitOutput(ctx, root, "cherry", target, branch)
	if err != nil {
		return false, err.Error()
	}
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if strings.HasPrefix(line, "+") {
			return false, "contains patches not present in " + target
		}
	}
	return true, "patch-equivalent"
}

func (s *Service) refExists(ctx context.Context, root, ref string) bool {
	return exec.CommandContext(ctx, s.Git, "-C", root, "show-ref", "--verify", "--quiet", ref).Run() == nil
}

func (s *Service) refsEqual(ctx context.Context, root, left, right string) bool {
	leftValue, leftErr := s.gitOutput(ctx, root, "rev-parse", left)
	rightValue, rightErr := s.gitOutput(ctx, root, "rev-parse", right)
	return leftErr == nil && rightErr == nil && strings.TrimSpace(leftValue) == strings.TrimSpace(rightValue)
}

func (s *Service) worktreeStatus(ctx context.Context, worktree string) ([]string, error) {
	output, err := exec.CommandContext(ctx, s.Git, "-C", worktree, "status", "--porcelain=v1", "-z").Output()
	if err != nil {
		return nil, fmt.Errorf("inspect worktree status: %w", err)
	}
	var dirty []string
	for _, record := range bytes.Split(output, []byte{0}) {
		if len(record) < 4 {
			continue
		}
		dirty = append(dirty, string(record[:2])+" "+string(record[3:]))
	}
	return dirty, nil
}

func (s *Service) CurrentTarget(ctx context.Context) (string, string) {
	pane := os.Getenv("TMUX_PANE")
	if pane == "" {
		return "", ""
	}
	output, err := exec.CommandContext(ctx, s.Tmux, "display-message", "-p", "-t", pane, "#{session_id}\037#{window_id}").Output()
	if err != nil {
		return "", ""
	}
	fields := strings.Split(strings.TrimSpace(string(output)), `\037`)
	if len(fields) != 2 {
		return "", ""
	}
	return fields[0], fields[1]
}

func (s *Service) CurrentSession(ctx context.Context) string {
	session, _ := s.CurrentTarget(ctx)
	return session
}

func (s *Service) Sessions(ctx context.Context) ([]Session, error) {
	format := "#{session_id}\037#{session_name}\037#{@fx_task_id}\037#{@fx_worktree}\037#{@fx_repository}\037#{@fx_workspace_id}\037#{@fx_workflow_role}\037#{window_id}\037#{window_name}\037#{@fx_task_role}"
	output, err := exec.CommandContext(ctx, s.Tmux, "list-panes", "-a", "-F", format).CombinedOutput()
	if err != nil {
		message := string(output)
		if strings.Contains(message, "no server running") || strings.Contains(message, "failed to connect") || strings.Contains(message, "error connecting") {
			return nil, nil
		}
		return nil, fmt.Errorf("list tmux panes: %s", commandError(output, err))
	}
	sessions := make([]Session, 0)
	seen := make(map[string]bool)
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		fields := strings.Split(line, `\037`)
		if len(fields) != 10 {
			continue
		}
		session := Session{
			ID: fields[0], Name: fields[1], TaskID: fields[2], Worktree: fields[3],
			Repository: fields[4], WorkspaceID: fields[5], Role: fields[6],
		}
		key := session.ID
		if session.Role == "workstream" && fields[9] == "task" {
			session.WindowID, session.WindowName = fields[7], fields[8]
			key += ":" + session.WindowID
		}
		if (session.TaskID == "" && session.Role != "task") || seen[key] {
			continue
		}
		seen[key] = true
		sessions = append(sessions, session)
	}
	return sessions, nil
}

func (s *Service) taskSessions(ctx context.Context, taskID string) ([]Session, error) {
	sessions, err := s.Sessions(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]Session, 0)
	for _, session := range sessions {
		if session.TaskID == taskID {
			result = append(result, session)
		}
	}
	return result, nil
}

func (s *Service) KillSession(ctx context.Context, session Session) error {
	command, target, label := "kill-session", session.ID, "session "+session.Name
	if session.WindowID != "" {
		command, target, label = "kill-window", session.WindowID, "window "+session.Name+":"+session.WindowName
	}
	if output, err := exec.CommandContext(ctx, s.Tmux, command, "-t", target).CombinedOutput(); err != nil {
		return fmt.Errorf("kill tmux %s: %s", label, commandError(output, err))
	}
	return nil
}

func (s *Service) gitOutput(ctx context.Context, root string, args ...string) (string, error) {
	arguments := append([]string{"-C", root}, args...)
	output, err := exec.CommandContext(ctx, s.Git, arguments...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s", commandError(output, err))
	}
	return string(output), nil
}

func canonical(path string) string {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	if resolved, err := filepath.EvalSymlinks(absolute); err == nil {
		return resolved
	}
	return filepath.Clean(absolute)
}

func commandError(output []byte, err error) string {
	if message := strings.TrimSpace(string(output)); message != "" {
		return message
	}
	return err.Error()
}
