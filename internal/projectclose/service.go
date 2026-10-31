package projectclose

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jarneson/fx-workbench/internal/cleanup"
	"github.com/jarneson/fx-workbench/internal/workload"
)

type Catalog interface {
	FindType(string, string, string) (workload.Entity, error)
	List(workload.ListOptions) ([]workload.Entity, error)
	SetProjectClosed(workload.Entity, []workload.Entity, bool) error
}

type Runtime interface {
	ScanWorktrees(context.Context, string, []workload.Entity) ([]cleanup.WorktreePlan, error)
	Sessions(context.Context) ([]cleanup.Session, error)
	CurrentSession(context.Context) string
	KillSession(context.Context, cleanup.Session) error
}

type Plan struct {
	Project       workload.Entity        `json:"project"`
	Workstreams   []workload.Entity      `json:"workstreams"`
	Tasks         []workload.Entity      `json:"tasks"`
	Worktrees     []cleanup.WorktreePlan `json:"worktrees,omitempty"`
	Sessions      []cleanup.Session      `json:"sessions,omitempty"`
	CloseSessions bool                   `json:"close_sessions"`
	Blockers      []string               `json:"blockers,omitempty"`
	Eligible      bool                   `json:"eligible"`
}

type Result struct {
	Plan           Plan `json:"plan"`
	SessionsClosed int  `json:"sessions_closed"`
	Closed         bool `json:"closed"`
}

func Build(ctx context.Context, catalog Catalog, runtime Runtime, projectID string, closeSessions bool) (Plan, error) {
	if projectID == "" {
		var err error
		projectID, err = workload.CurrentProject()
		if err != nil {
			return Plan{}, err
		}
	}
	project, err := catalog.FindType(projectID, projectID, "project")
	if err != nil {
		return Plan{}, err
	}
	plan := Plan{Project: project, CloseSessions: closeSessions}
	if project.Legacy || project.Schema != workload.SchemaV1 {
		plan.Blockers = append(plan.Blockers, "project must be migrated to fx-workbench/v1 before closeout")
	}
	if project.Status == "closed" {
		plan.Blockers = append(plan.Blockers, "project is already closed")
	}
	plan.Workstreams, err = catalog.List(workload.ListOptions{Type: "workstream", Project: projectID, IncludeClosed: true})
	if err != nil {
		return Plan{}, err
	}
	plan.Tasks, err = catalog.List(workload.ListOptions{Type: "task", Project: projectID, IncludeClosed: true})
	if err != nil {
		return Plan{}, err
	}
	statusCounts := make(map[string]int)
	for _, task := range plan.Tasks {
		if task.Status != "merged" && task.Status != "superseded" {
			statusCounts[task.Status]++
		}
	}
	if len(statusCounts) > 0 {
		var statuses []string
		for status, count := range statusCounts {
			statuses = append(statuses, fmt.Sprintf("%s=%d", status, count))
		}
		sort.Strings(statuses)
		plan.Blockers = append(plan.Blockers, "tasks are not merged/superseded: "+strings.Join(statuses, ", "))
	}
	seenRepository := make(map[string]bool)
	for _, repository := range project.Repositories {
		canonical := canonicalPath(repository)
		if canonical == "" || seenRepository[canonical] {
			continue
		}
		seenRepository[canonical] = true
		worktrees, scanErr := runtime.ScanWorktrees(ctx, repository, plan.Tasks)
		if scanErr != nil {
			plan.Blockers = append(plan.Blockers, fmt.Sprintf("scan repository %s: %v", repository, scanErr))
			continue
		}
		for _, worktree := range worktrees {
			if worktree.TaskID == "" {
				continue
			}
			plan.Worktrees = append(plan.Worktrees, worktree)
			plan.Blockers = append(plan.Blockers, fmt.Sprintf("task worktree remains: %s (%s)", worktree.TaskID, worktree.Path))
		}
	}
	sessions, sessionErr := runtime.Sessions(ctx)
	if sessionErr != nil {
		plan.Blockers = append(plan.Blockers, "inspect tmux sessions: "+sessionErr.Error())
	} else {
		taskIDs := make(map[string]bool, len(plan.Tasks))
		for _, task := range plan.Tasks {
			taskIDs[task.ID] = true
		}
		root := canonicalPath(project.RootRepository)
		for _, session := range sessions {
			belongs := session.TaskID != "" && taskIDs[session.TaskID]
			if session.Role == "root" && canonicalPath(session.Repository) == root {
				belongs = true
			}
			if belongs {
				plan.Sessions = append(plan.Sessions, session)
			}
		}
	}
	if len(plan.Sessions) > 0 && !closeSessions {
		plan.Blockers = append(plan.Blockers, fmt.Sprintf("%d tagged project sessions remain; rerun with --close-sessions", len(plan.Sessions)))
	}
	if closeSessions {
		current := runtime.CurrentSession(ctx)
		for _, session := range plan.Sessions {
			if session.ID == current {
				plan.Blockers = append(plan.Blockers, "refusing to close the current tmux session; switch to another session first")
				break
			}
		}
	}
	plan.Eligible = len(plan.Blockers) == 0
	return plan, nil
}

func Execute(ctx context.Context, catalog Catalog, runtime Runtime, plan Plan) (Result, error) {
	result := Result{Plan: plan}
	if !plan.Eligible {
		return result, fmt.Errorf("project closeout plan is blocked")
	}
	if plan.CloseSessions {
		for _, session := range plan.Sessions {
			if err := runtime.KillSession(ctx, session); err != nil {
				return result, err
			}
			result.SessionsClosed++
		}
	}
	if err := catalog.SetProjectClosed(plan.Project, plan.Workstreams, true); err != nil {
		return result, err
	}
	result.Closed = true
	return result, nil
}

func Reopen(catalog Catalog, projectID string) error {
	project, err := catalog.FindType(projectID, projectID, "project")
	if err != nil {
		return err
	}
	if project.Status != "closed" {
		return fmt.Errorf("project %s is %q, not closed", project.ID, project.Status)
	}
	workstreams, err := catalog.List(workload.ListOptions{Type: "workstream", Project: projectID, IncludeClosed: true})
	if err != nil {
		return err
	}
	return catalog.SetProjectClosed(project, workstreams, false)
}

func canonicalPath(path string) string {
	if path == "" {
		return ""
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	if resolved, err := filepath.EvalSymlinks(absolute); err == nil {
		return resolved
	}
	return filepath.Clean(absolute)
}
