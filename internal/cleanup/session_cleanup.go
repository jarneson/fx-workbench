package cleanup

import (
	"context"
	"fmt"
	"os"
)

type SessionPlan struct {
	Session  Session  `json:"session"`
	Warnings []string `json:"warnings,omitempty"`
	Blockers []string `json:"blockers,omitempty"`
	Eligible bool     `json:"eligible"`
}

func (s *Service) PlanSessionCleanup(ctx context.Context, sessionID string) SessionPlan {
	plan := SessionPlan{}
	sessions, err := s.Sessions(ctx)
	if err != nil {
		plan.Blockers = append(plan.Blockers, err.Error())
		return plan
	}
	for _, session := range sessions {
		legacyMatch := session.WindowID == "" && (session.ID == sessionID || session.Name == sessionID)
		windowMatch := session.WindowID == sessionID || session.WindowName == sessionID
		if legacyMatch || windowMatch {
			plan.Session = session
			break
		}
	}
	if plan.Session.ID == "" {
		plan.Blockers = append(plan.Blockers, "tmux session no longer exists")
		return plan
	}
	if plan.Session.Role != "task" && !(plan.Session.Role == "workstream" && plan.Session.WindowID != "") {
		plan.Blockers = append(plan.Blockers, fmt.Sprintf("tmux target role is %q, not task", plan.Session.Role))
	}
	currentSession, currentWindow := s.CurrentTarget(ctx)
	if plan.Session.ID == currentSession && (plan.Session.WindowID == "" || plan.Session.WindowID == currentWindow) {
		plan.Blockers = append(plan.Blockers, "refusing to clean the current tmux task target; switch task windows first")
	}
	if plan.Session.Worktree != "" {
		if _, err := os.Stat(plan.Session.Worktree); err == nil {
			plan.Blockers = append(plan.Blockers, "session worktree path still exists; use worktree cleanup")
		} else if !os.IsNotExist(err) {
			plan.Blockers = append(plan.Blockers, "inspect session worktree: "+err.Error())
		}
	}
	plan.Warnings = append(plan.Warnings, "session-only cleanup kills tmux state but does not alter Git")
	plan.Eligible = len(plan.Blockers) == 0
	return plan
}

func (s *Service) ExecuteSessionCleanup(ctx context.Context, plan SessionPlan) error {
	if !plan.Eligible {
		return fmt.Errorf("session cleanup plan is blocked")
	}
	return s.KillSession(ctx, plan.Session)
}
