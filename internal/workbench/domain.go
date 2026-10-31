package workbench

import (
	"cmp"
	"slices"
	"time"
)

type AgentState string

const (
	StateAttention AgentState = "needs-attention"
	StateRunning   AgentState = "running"
	StateIdle      AgentState = "idle"
	StateUnknown   AgentState = "unknown"
)

type Pane struct {
	ID             string
	Index          string
	Active         bool
	Command        string
	Path           string
	AgentState     AgentState
	AgentTitle     string
	TaskID         string
	FXSessionID    string
	NeedsAttention bool
}

type Window struct {
	ID          string
	Index       string
	Name        string
	Active      bool
	Role        string
	TaskID      string
	Repository  string
	Worktree    string
	Branch      string
	FXSessionID string
	Panes       []Pane
}

type Session struct {
	ID          string
	Name        string
	Attached    bool
	CreatedAt   time.Time
	Path        string
	Role        string
	Parent      string
	ParentName  string
	WorkspaceID string
	Project     string
	Workstream  string
	Repository  string
	Worktree    string
	TaskID      string
	Explicit    bool
	State       AgentState
	Windows     []Window
	Agents      int
}

type Snapshot struct {
	Sessions   []Session
	CapturedAt time.Time
}

func rank(state AgentState) int {
	switch state {
	case StateAttention:
		return 0
	case StateRunning:
		return 1
	case StateIdle:
		return 2
	default:
		return 3
	}
}

func normalize(sessions []Session) []Session {
	for si := range sessions {
		session := &sessions[si]
		session.State = StateUnknown
		for wi := range session.Windows {
			window := &session.Windows[wi]
			for pi := range window.Panes {
				pane := &window.Panes[pi]
				if pane.Active || session.Path == "" {
					session.Path = pane.Path
				}
				if pane.AgentState != StateUnknown || pane.AgentTitle != "" {
					session.Agents++
				}
				state := pane.AgentState
				if pane.NeedsAttention {
					state = StateAttention
				}
				if rank(state) < rank(session.State) {
					session.State = state
				}
			}
		}
		inferIdentity(session)
	}

	for si := range sessions {
		if sessions[si].Parent == "" {
			continue
		}
		for _, candidate := range sessions {
			if candidate.ID == sessions[si].Parent || candidate.Name == sessions[si].Parent {
				sessions[si].ParentName = candidate.Name
				break
			}
		}
	}

	slices.SortStableFunc(sessions, func(a, b Session) int {
		if n := cmp.Compare(rank(a.State), rank(b.State)); n != 0 {
			return n
		}
		if a.Attached != b.Attached {
			if a.Attached {
				return -1
			}
			return 1
		}
		return cmp.Compare(a.Name, b.Name)
	})
	return sessions
}
