package cleanup

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jarneson/fx-workbench/internal/workload"
)

const (
	InventoryVerify       = "verify"
	InventoryStaleSession = "stale-session"
	InventoryBlocked      = "blocked"
)

type InventoryEntry struct {
	Path        string   `json:"path"`
	Branch      string   `json:"branch"`
	TaskID      string   `json:"task_id,omitempty"`
	Project     string   `json:"project,omitempty"`
	Status      string   `json:"status,omitempty"`
	Category    string   `json:"category"`
	SessionID   string   `json:"session_id,omitempty"`
	SessionName string   `json:"session_name,omitempty"`
	Reasons     []string `json:"reasons,omitempty"`
}

func (s *Service) Inventory(ctx context.Context, repository string, tasks []workload.Entity) ([]InventoryEntry, error) {
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
	tasksByID := make(map[string]workload.Entity)
	for _, task := range tasks {
		if task.Repository != "" && canonical(task.Repository) == canonical(rootWorktree) {
			tasksByID[task.ID] = task
			if task.Branch != "" {
				tasksByBranch[task.Branch] = append(tasksByBranch[task.Branch], task)
			}
		}
	}
	entries := make([]InventoryEntry, 0, max(0, len(worktrees)-1))
	registeredPaths := make(map[string]bool, len(worktrees))
	for _, worktree := range worktrees {
		registeredPaths[canonical(worktree.Path)] = true
		if canonical(worktree.Path) == canonical(rootWorktree) {
			continue
		}
		branch := strings.TrimPrefix(worktree.Branch, "refs/heads/")
		entry := InventoryEntry{Path: worktree.Path, Branch: branch, Category: InventoryBlocked}
		matches := tasksByBranch[branch]
		switch len(matches) {
		case 0:
			entry.Reasons = []string{"no canonical task maps to this worktree branch"}
		case 1:
			entry.TaskID = matches[0].ID
			entry.Project = matches[0].Project
			entry.Status = matches[0].Status
			if matches[0].Status == "merged" {
				entry.Category = InventoryVerify
			} else {
				entry.Reasons = []string{fmt.Sprintf("task status is %q, not merged", matches[0].Status)}
			}
		default:
			entry.Reasons = []string{"multiple canonical tasks map to this worktree branch"}
		}
		entries = append(entries, entry)
	}
	sessions, sessionErr := s.Sessions(ctx)
	if sessionErr != nil {
		return nil, sessionErr
	}
	for _, session := range sessions {
		isTaskTarget := session.Role == "task" || (session.Role == "workstream" && session.WindowID != "")
		if !isTaskTarget || session.Worktree == "" || canonical(session.Repository) != canonical(rootWorktree) {
			continue
		}
		if registeredPaths[canonical(session.Worktree)] {
			continue
		}
		branch := filepath.Base(session.Worktree)
		entry := InventoryEntry{
			Path: session.Worktree, Branch: branch, Category: InventoryStaleSession,
			SessionID: session.ID, SessionName: session.Name,
			Reasons: []string{"tmux task session points to a missing or unregistered Git worktree"},
		}
		if task, ok := tasksByID[session.TaskID]; ok {
			entry.TaskID, entry.Project, entry.Status = task.ID, task.Project, task.Status
		} else if matches := tasksByBranch[branch]; len(matches) == 1 {
			entry.TaskID, entry.Project, entry.Status = matches[0].ID, matches[0].Project, matches[0].Status
		}
		entries = append(entries, entry)
	}
	sort.SliceStable(entries, func(i, j int) bool {
		rank := map[string]int{InventoryVerify: 0, InventoryStaleSession: 1, InventoryBlocked: 2}
		if rank[entries[i].Category] != rank[entries[j].Category] {
			return rank[entries[i].Category] < rank[entries[j].Category]
		}
		return entries[i].Branch < entries[j].Branch
	})
	return entries, nil
}
