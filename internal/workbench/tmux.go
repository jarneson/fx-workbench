package workbench

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/jarneson/fx-workbench/internal/mainsync"
)

const (
	formatSeparator = "\x1f"
	outputSeparator = `\037` // tmux escapes control characters in formatted output.
)

const paneFormat = "#{session_id}" + formatSeparator +
	"#{session_name}" + formatSeparator +
	"#{session_attached}" + formatSeparator +
	"#{session_created}" + formatSeparator +
	"#{@fx_workflow_role}" + formatSeparator +
	"#{@fx_parent_session}" + formatSeparator +
	"#{@fx_workspace_id}" + formatSeparator +
	"#{@fx_repository}" + formatSeparator +
	"#{@fx_worktree}" + formatSeparator +
	"#{@fx_task_id}" + formatSeparator +
	"#{@fx_project_id}" + formatSeparator +
	"#{@fx_workstream_id}" + formatSeparator +
	"#{window_id}" + formatSeparator +
	"#{window_index}" + formatSeparator +
	"#{window_name}" + formatSeparator +
	"#{window_active}" + formatSeparator +
	"#{@fx_task_role}" + formatSeparator +
	"#{@fx_task_id}" + formatSeparator +
	"#{@fx_repository}" + formatSeparator +
	"#{@fx_worktree}" + formatSeparator +
	"#{@fx_branch}" + formatSeparator +
	"#{@fx_session_id}" + formatSeparator +
	"#{pane_id}" + formatSeparator +
	"#{pane_index}" + formatSeparator +
	"#{pane_active}" + formatSeparator +
	"#{pane_current_command}" + formatSeparator +
	"#{pane_current_path}" + formatSeparator +
	"#{@fx_state}" + formatSeparator +
	"#{@fx_session_title}" + formatSeparator +
	"#{@fx_pane_attention}" + formatSeparator +
	"#{@fx_session_id}"

type Source interface {
	Snapshot(context.Context) (Snapshot, error)
	Focus(context.Context, Session) error
}

type TmuxSource struct {
	binary   string
	client   string
	mainSync mainsync.Synchronizer
}

func NewTmuxSource(binary string) *TmuxSource {
	if binary == "" {
		binary = os.Getenv("FX_WORKBENCH_TMUX_BIN")
	}
	if binary == "" {
		binary = "tmux"
	}
	client := os.Getenv("FX_WORKBENCH_CLIENT")
	if strings.Contains(client, "#{") {
		client = ""
	}
	return &TmuxSource{binary: binary, client: client, mainSync: mainsync.New()}
}

func (s *TmuxSource) Snapshot(ctx context.Context) (Snapshot, error) {
	output, err := exec.CommandContext(ctx, s.binary, "list-panes", "-a", "-F", paneFormat).Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return Snapshot{}, fmt.Errorf("tmux list-panes: %s", strings.TrimSpace(string(exitErr.Stderr)))
		}
		return Snapshot{}, fmt.Errorf("tmux list-panes: %w", err)
	}
	return parseSnapshot(output, time.Now()), nil
}

func (s *TmuxSource) Focus(ctx context.Context, session Session) error {
	if os.Getenv("TMUX") == "" && s.client == "" {
		return errors.New("focus requires running fx-workbench inside tmux")
	}
	args := []string{"switch-client"}
	if s.client != "" {
		args = append(args, "-c", s.client)
	}
	args = append(args, "-t", session.ID)
	if output, err := exec.CommandContext(ctx, s.binary, args...).CombinedOutput(); err != nil {
		return fmt.Errorf("tmux switch-client: %s", strings.TrimSpace(string(output)))
	}
	windowID, paneID := focusTarget(session)
	if windowID != "" {
		if output, err := exec.CommandContext(ctx, s.binary, "select-window", "-t", windowID).CombinedOutput(); err != nil {
			return fmt.Errorf("tmux select-window: %s", strings.TrimSpace(string(output)))
		}
	}
	if paneID != "" {
		if output, err := exec.CommandContext(ctx, s.binary, "select-pane", "-t", paneID).CombinedOutput(); err != nil {
			return fmt.Errorf("tmux select-pane: %s", strings.TrimSpace(string(output)))
		}
	}
	return nil
}

func focusTarget(session Session) (string, string) {
	bestRank := 100
	bestActive := false
	windowID, paneID := "", ""
	for _, window := range session.Windows {
		for _, pane := range window.Panes {
			if pane.AgentTitle == "" && pane.AgentState == StateUnknown {
				continue
			}
			state := pane.AgentState
			if pane.NeedsAttention {
				state = StateAttention
			}
			candidateRank := rank(state)
			candidateActive := pane.Active
			if candidateRank < bestRank || (candidateRank == bestRank && candidateActive && !bestActive) {
				bestRank, bestActive = candidateRank, candidateActive
				windowID, paneID = window.ID, pane.ID
			}
		}
	}
	if windowID == "" {
		for _, window := range session.Windows {
			if len(window.Panes) > 0 {
				return window.ID, window.Panes[0].ID
			}
		}
	}
	return windowID, paneID
}

func parseSnapshot(output []byte, capturedAt time.Time) Snapshot {
	sessions := make([]Session, 0)
	sessionByID := make(map[string]int)
	windows := make(map[string]struct{ session, window int })

	for _, raw := range bytes.Split(bytes.TrimSpace(output), []byte("\n")) {
		if len(raw) == 0 {
			continue
		}
		fields := strings.Split(string(raw), outputSeparator)
		if len(fields) != 31 {
			continue
		}

		si, ok := sessionByID[fields[0]]
		if !ok {
			created, _ := strconv.ParseInt(fields[3], 10, 64)
			si = len(sessions)
			sessionByID[fields[0]] = si
			worktree, taskID := fields[8], fields[9]
			if fields[4] == RoleWorkstream {
				worktree, taskID = "", ""
			}
			sessions = append(sessions, Session{
				ID: fields[0], Name: fields[1], Attached: fields[2] != "0",
				CreatedAt: time.Unix(created, 0), Role: fields[4], Parent: fields[5],
				WorkspaceID: fields[6], Repository: fields[7], Worktree: worktree, TaskID: taskID,
				Project: fields[10], Workstream: fields[11],
			})
		}

		windowKey := fields[0] + outputSeparator + fields[12]
		position, ok := windows[windowKey]
		if !ok {
			position = struct{ session, window int }{si, len(sessions[si].Windows)}
			windows[windowKey] = position
			window := Window{ID: fields[12], Index: fields[13], Name: fields[14], Active: fields[15] != "0"}
			if fields[16] == RoleTask && fields[17] != "" {
				window.Role, window.TaskID = RoleTask, fields[17]
				window.Repository, window.Worktree, window.Branch = fields[18], fields[19], fields[20]
				window.FXSessionID = fields[21]
			}
			sessions[si].Windows = append(sessions[si].Windows, window)
		}

		state := AgentState(fields[27])
		if state == "" {
			state = StateUnknown
		}
		window := &sessions[position.session].Windows[position.window]
		window.Panes = append(window.Panes, Pane{
			ID: fields[22], Index: fields[23], Active: fields[24] != "0",
			Command: fields[25], Path: fields[26], AgentState: state,
			AgentTitle: fields[28], TaskID: fields[17], FXSessionID: fields[30],
			NeedsAttention: fields[29] == "1",
		})
	}

	return Snapshot{Sessions: normalize(sessions), CapturedAt: capturedAt}
}
