package taskui

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/jarneson/fx-workbench/internal/profiles"
	"github.com/jarneson/fx-workbench/internal/promptdelivery"
	"github.com/jarneson/fx-workbench/internal/tasklaunch"
	"github.com/jarneson/fx-workbench/internal/workbench"
	"github.com/jarneson/fx-workbench/internal/workload"
)

type CommandActions struct {
	Executable string
}

func NewCommandActions() *CommandActions {
	executable, _ := os.Executable()
	return &CommandActions{Executable: executable}
}

func (actions *CommandActions) run(ctx context.Context, args ...string) (string, error) {
	if actions.Executable == "" {
		return "", errors.New("resolve fx-workbench executable")
	}
	output, err := exec.CommandContext(ctx, actions.Executable, args...).CombinedOutput()
	message := strings.TrimSpace(string(output))
	if err != nil {
		if message != "" {
			return message, fmt.Errorf("%s", message)
		}
		return "", err
	}
	return message, nil
}

func (actions *CommandActions) OpenPlanner(ctx context.Context, workstream workload.Entity) (string, error) {
	if workstream.Type != "workstream" || workstream.Project == "" {
		return "", errors.New("select a workstream before opening its planner")
	}
	return actions.run(ctx, "plan", "--project", workstream.Project, "--workstream", workstream.ID)
}

func (actions *CommandActions) Inspect(ctx context.Context, entity workload.Entity) (string, error) {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "less"
	}
	var command *exec.Cmd
	if os.Getenv("TMUX") != "" {
		command = exec.CommandContext(ctx, "tmux", "display-popup", "-E", editor, entity.SourcePath)
	} else {
		parts := strings.Fields(editor)
		if len(parts) == 0 {
			return "", errors.New("EDITOR is empty")
		}
		command = exec.CommandContext(ctx, parts[0], append(parts[1:], entity.SourcePath)...)
		command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	}
	if err := command.Run(); err != nil {
		return "", fmt.Errorf("inspect source: %w", err)
	}
	return "source closed: " + entity.SourcePath, nil
}

func (actions *CommandActions) Preflight(ctx context.Context, task workload.Entity) (string, error) {
	return actions.run(ctx, "launch", "--project", task.Project, "--dry-run", task.SourcePath)
}

func (actions *CommandActions) Launch(ctx context.Context, task workload.Entity) (string, error) {
	return actions.run(ctx, "launch", "--project", task.Project, task.SourcePath)
}

type CommandSidebarActions struct {
	Runtime         Runtime
	Store           *workload.Store
	WorkbenchBinary string
	TmuxBinary      string
	TuicrBinary     string
	GHBinary        string
	GitBinary       string
	FXCommand       string
	Pause           func(context.Context, time.Duration) error
}

func NewCommandSidebarActions(runtime Runtime) *CommandSidebarActions {
	tmuxBinary := os.Getenv("FX_WORKBENCH_TMUX_BIN")
	if tmuxBinary == "" {
		tmuxBinary = "tmux"
	}
	tuicrBinary := os.Getenv("FX_WORKBENCH_TUICR_BIN")
	if tuicrBinary == "" {
		tuicrBinary = "tuicr"
	}
	ghBinary := os.Getenv("FX_WORKBENCH_GH_BIN")
	if ghBinary == "" {
		ghBinary = "gh"
	}
	gitBinary := os.Getenv("FX_WORKBENCH_GIT_BIN")
	if gitBinary == "" {
		gitBinary = "git"
	}
	fxCommand := os.Getenv("FX_TASK_FX_COMMAND")
	if fxCommand == "" {
		fxCommand = "fx-tmux"
	}
	workbenchBinary, _ := os.Executable()
	return &CommandSidebarActions{Runtime: runtime, Store: workload.NewStore(), WorkbenchBinary: workbenchBinary, TmuxBinary: tmuxBinary, TuicrBinary: tuicrBinary, GHBinary: ghBinary, GitBinary: gitBinary, FXCommand: fxCommand, Pause: promptdelivery.Pause}
}

func (actions *CommandSidebarActions) Focus(ctx context.Context, _ workload.Entity, target workbench.Session) error {
	return actions.Runtime.Focus(ctx, target)
}

func (actions *CommandSidebarActions) Resume(ctx context.Context, task workload.Entity) error {
	if task.FXSessionID == "" {
		return fmt.Errorf("task %s has no fx session to resume", task.ID)
	}
	pane := os.Getenv("TMUX_PANE")
	if pane == "" {
		return errors.New("task resume requires running inside the workstream tmux session")
	}
	output, err := exec.CommandContext(ctx, actions.TmuxBinary, "display-message", "-p", "-t", pane, "#{session_id}").CombinedOutput()
	if err != nil {
		return fmt.Errorf("resolve workstream session: %s", strings.TrimSpace(string(output)))
	}
	sessionID := strings.TrimSpace(string(output))
	if sessionID == "" {
		return errors.New("resolve workstream session returned an empty identity")
	}
	gitBinary := actions.GitBinary
	if gitBinary == "" {
		gitBinary = "git"
	}
	path, err := resolveTaskReviewPath(ctx, gitBinary, task.Repository, task.Branch)
	if err != nil {
		return err
	}
	agent, err := actions.ensureTaskAgent(ctx, task, sessionID, path)
	if err != nil {
		return err
	}
	return actions.Runtime.Focus(ctx, agent)
}

func (actions *CommandSidebarActions) Review(ctx context.Context, task workload.Entity, target workbench.Session) error {
	windowID := ""
	splitTarget := ""
	path := task.Repository
	taggedTarget := target.ID != "" && len(target.Windows) == 1 && len(target.Windows[0].Panes) == 1 && target.Windows[0].Panes[0].TaskID == task.ID
	if taggedTarget {
		pane := target.Windows[0].Panes[0]
		if pane.Path != "" {
			path = pane.Path
		}
		if pane.FXSessionID == "" {
			agent, err := actions.ensureTaskAgent(ctx, task, target.ID, path)
			if err != nil {
				return err
			}
			target = agent
		}
		if err := actions.Runtime.Focus(ctx, target); err != nil {
			return err
		}
		windowID = target.Windows[0].ID
		splitTarget = target.Windows[0].Panes[0].ID
	} else {
		for _, window := range target.Windows {
			for _, pane := range window.Panes {
				if pane.TaskID == task.ID {
					return errors.New("review target contains multiple task panes")
				}
			}
		}
		pane := os.Getenv("TMUX_PANE")
		if pane == "" {
			return errors.New("offline self-review requires running inside the workstream tmux session")
		}
		output, err := exec.CommandContext(ctx, actions.TmuxBinary, "display-message", "-p", "-t", pane, "#{session_id}").CombinedOutput()
		if err != nil {
			return fmt.Errorf("resolve workstream session: %s", strings.TrimSpace(string(output)))
		}
		sessionID := strings.TrimSpace(string(output))
		if sessionID == "" {
			return errors.New("resolve workstream session returned an empty identity")
		}
		gitBinary := actions.GitBinary
		if gitBinary == "" {
			gitBinary = "git"
		}
		path, err = resolveTaskReviewPath(ctx, gitBinary, task.Repository, task.Branch)
		if err != nil {
			return err
		}
		agent, err := actions.ensureTaskAgent(ctx, task, sessionID, path)
		if err != nil {
			return err
		}
		if err := actions.Runtime.Focus(ctx, agent); err != nil {
			return err
		}
		windowID = agent.Windows[0].ID
		splitTarget = agent.Windows[0].Panes[0].ID
		path = agent.Windows[0].Panes[0].Path
	}
	if windowID == "" || path == "" {
		return errors.New("review window and repository path are required")
	}
	output, err := exec.CommandContext(ctx, actions.TmuxBinary, "list-panes", "-t", windowID, "-F", "#{pane_id}|#{@fx_task_review}|#{@fx_task_id}").CombinedOutput()
	if err != nil {
		return fmt.Errorf("list review panes: %s", strings.TrimSpace(string(output)))
	}
	reviewPane := ""
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		fields := strings.Split(line, "|")
		if len(fields) != 3 {
			continue
		}
		if fields[1] == "1" && fields[2] == task.ID {
			reviewPane = fields[0]
		}
	}
	if reviewPane == "" {
		if splitTarget == "" {
			return errors.New("review requires an exact task agent pane")
		}
		workbenchBinary := actions.WorkbenchBinary
		if workbenchBinary == "" {
			workbenchBinary = "fx-workbench"
		}
		output, err = exec.CommandContext(ctx, actions.TmuxBinary, "split-window", "-b", "-h", "-l", "60%", "-P", "-F", "#{pane_id}", "-t", splitTarget, "-c", path, workbenchBinary, "review-relay", "--target-pane", splitTarget, "--task-id", task.ID, "--tmux", actions.TmuxBinary, "--", actions.TuicrBinary, "--no-update-check").CombinedOutput()
		if err != nil {
			return fmt.Errorf("open tuicr split: %s", strings.TrimSpace(string(output)))
		}
		reviewPane = strings.TrimSpace(string(output))
		for option, value := range map[string]string{"@fx_task_review": "1", "@fx_task_id": task.ID} {
			if output, err = exec.CommandContext(ctx, actions.TmuxBinary, "set-option", "-p", "-t", reviewPane, option, value).CombinedOutput(); err != nil {
				return fmt.Errorf("tag review pane: %s", strings.TrimSpace(string(output)))
			}
		}
	}
	if output, err = exec.CommandContext(ctx, actions.TmuxBinary, "select-pane", "-t", reviewPane, "-T", "review").CombinedOutput(); err != nil {
		return fmt.Errorf("focus review pane: %s", strings.TrimSpace(string(output)))
	}
	return nil
}

func (actions *CommandSidebarActions) ensureTaskAgent(ctx context.Context, task workload.Entity, sessionID, path string) (workbench.Session, error) {
	canonicalWorktree, err := canonicalTaskPath(path)
	if err != nil {
		return workbench.Session{}, fmt.Errorf("resolve task worktree: %w", err)
	}
	format := strings.Join([]string{
		"#{window_id}", "#{pane_id}", "#{pane_dead}", "#{pane_current_path}", "#{@fx_state}", "#{@fx_session_id}",
		"#{@fx_task_role}", "#{@fx_task_id}", "#{@fx_worktree}", "#{@fx_task_review}", "#{@fx_planner}", "#{@fx_planner_role}",
	}, "|")
	output, err := exec.CommandContext(ctx, actions.TmuxBinary, "list-panes", "-s", "-t", sessionID, "-F", format).CombinedOutput()
	if err != nil {
		return workbench.Session{}, fmt.Errorf("find task agent pane: %s", strings.TrimSpace(string(output)))
	}
	type paneIdentity struct {
		window, pane, fxSessionID string
	}
	var candidates []paneIdentity
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		fields := strings.Split(line, "|")
		if len(fields) != 12 || fields[2] != "0" {
			continue
		}
		if fields[6] == "task" && fields[7] == task.ID && fields[9] == "" {
			if fields[8] != "" {
				taggedWorktree, pathErr := canonicalTaskPath(fields[8])
				if pathErr != nil || taggedWorktree != canonicalWorktree {
					return workbench.Session{}, fmt.Errorf("live task agent for %s uses unexpected worktree %s (wanted %s)", task.ID, fields[8], canonicalWorktree)
				}
			}
			fxSessionID := fields[5]
			if fxSessionID == "" {
				fxSessionID, err = actions.waitForTaskAgent(ctx, fields[1], "")
				if err != nil {
					return workbench.Session{}, err
				}
				if err := actions.publishTaskAgentSession(ctx, task, fields[0], fields[1], fxSessionID, true); err != nil {
					return workbench.Session{}, err
				}
				if err := actions.sendTaskGuidance(ctx, task, fields[1]); err != nil {
					return workbench.Session{}, err
				}
			} else if task.FXSessionID != fxSessionID {
				if err := actions.persistTaskAgentSession(task, fxSessionID); err != nil {
					return workbench.Session{}, err
				}
			}
			return taskAgentSession(sessionID, fields[0], fields[1], canonicalWorktree), nil
		}
		if fields[4] == "" || fields[5] == "" || fields[6] != "" || fields[7] != "" || fields[9] != "" || fields[10] != "" || fields[11] != "" {
			continue
		}
		candidatePath, pathErr := canonicalTaskPath(fields[3])
		if pathErr == nil && candidatePath == canonicalWorktree {
			candidates = append(candidates, paneIdentity{window: fields[0], pane: fields[1], fxSessionID: fields[5]})
		}
	}
	if len(candidates) > 1 {
		panes := make([]string, len(candidates))
		for index, candidate := range candidates {
			panes[index] = candidate.pane
		}
		return workbench.Session{}, fmt.Errorf("multiple untagged fx panes match task worktree %s: %s", canonicalWorktree, strings.Join(panes, ", "))
	}
	if len(candidates) == 1 {
		candidate := candidates[0]
		task.FXSessionID = candidate.fxSessionID
		if err := actions.tagTaskAgent(ctx, task, candidate.window, candidate.pane, canonicalWorktree); err != nil {
			return workbench.Session{}, err
		}
		if err := actions.persistTaskAgentSession(task, candidate.fxSessionID); err != nil {
			return workbench.Session{}, err
		}
		if err := actions.sendTaskGuidance(ctx, task, candidate.pane); err != nil {
			return workbench.Session{}, err
		}
		return taskAgentSession(sessionID, candidate.window, candidate.pane, canonicalWorktree), nil
	}

	resumeSessionID := task.FXSessionID
	agent, err := actions.launchTaskAgent(ctx, task, sessionID, canonicalWorktree, resumeSessionID)
	if err == nil || resumeSessionID == "" || !isTerminalTaskAgentStartup(err) {
		return agent, err
	}
	if cleanupErr := actions.removeFailedTaskAgent(ctx, task.ID, agent); cleanupErr != nil {
		return workbench.Session{}, fmt.Errorf("resume fx session %s failed: %v; remove failed task window: %w", resumeSessionID, err, cleanupErr)
	}
	task.FXSessionID = ""
	replacement, replacementErr := actions.launchTaskAgent(ctx, task, sessionID, canonicalWorktree, "")
	if replacementErr != nil {
		return workbench.Session{}, fmt.Errorf("resume fx session %s failed: %v; fresh task agent also failed: %w", resumeSessionID, err, replacementErr)
	}
	return replacement, nil
}

func canonicalTaskPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(absolute)
}

func taskAgentSession(sessionID, windowID, paneID, path string) workbench.Session {
	return workbench.Session{ID: sessionID, Windows: []workbench.Window{{ID: windowID, Panes: []workbench.Pane{{ID: paneID, Path: path}}}}}
}

type taskAgentStartupError struct {
	pane     string
	detail   string
	terminal bool
	err      error
}

func (err *taskAgentStartupError) Error() string {
	message := "task agent in pane " + err.pane + " did not become ready"
	if err.detail != "" {
		message += ": " + err.detail
	}
	if err.err != nil {
		message += ": " + err.err.Error()
	}
	return message
}

func (err *taskAgentStartupError) Unwrap() error { return err.err }

func isTerminalTaskAgentStartup(err error) bool {
	var startupErr *taskAgentStartupError
	return errors.As(err, &startupErr) && startupErr.terminal
}

func (actions *CommandSidebarActions) launchTaskAgent(ctx context.Context, task workload.Entity, sessionID, path, resumeSessionID string) (workbench.Session, error) {
	fxCommand := actions.FXCommand
	if fxCommand == "" {
		fxCommand = "fx-tmux"
	}
	windowName := task.Title
	if windowName == "" {
		windowName = task.ID
	}
	args := []string{"new-window", "-d", "-P", "-F", "#{window_id}|#{pane_id}", "-t", sessionID + ":", "-n", windowName, "-c", path, fxCommand}
	operation := "start"
	if resumeSessionID != "" {
		args = append(args, "--resume", resumeSessionID)
		operation = "resume"
	}
	output, err := exec.CommandContext(ctx, actions.TmuxBinary, args...).CombinedOutput()
	if err != nil {
		return workbench.Session{}, fmt.Errorf("%s task agent: %s", operation, commandOutput(output, err))
	}
	identity := strings.Split(strings.TrimSpace(string(output)), "|")
	if len(identity) != 2 || identity[0] == "" || identity[1] == "" {
		return workbench.Session{}, fmt.Errorf("%s task agent returned malformed identity: %q", operation, strings.TrimSpace(string(output)))
	}
	windowID, paneID := identity[0], identity[1]
	agent := taskAgentSession(sessionID, windowID, paneID, path)
	startupTask := task
	startupTask.FXSessionID = ""
	if err := actions.tagTaskAgent(ctx, startupTask, windowID, paneID, path); err != nil {
		if ctx.Err() == nil {
			if terminal, detail := actions.taskAgentTerminalState(ctx, paneID, "", "", err, err); terminal {
				return agent, &taskAgentStartupError{pane: paneID, detail: detail + "; identity tagging failed: " + err.Error(), terminal: true}
			}
		}
		return agent, err
	}
	fxSessionID, err := actions.waitForTaskAgent(ctx, paneID, resumeSessionID)
	if err != nil {
		return agent, err
	}
	if err := actions.publishTaskAgentSession(ctx, task, windowID, paneID, fxSessionID, resumeSessionID == ""); err != nil {
		return agent, err
	}
	if err := actions.sendTaskGuidance(ctx, task, paneID); err != nil {
		return agent, err
	}
	return agent, nil
}

func commandOutput(output []byte, err error) string {
	message := strings.TrimSpace(string(output))
	if message != "" {
		return message
	}
	return err.Error()
}

func (actions *CommandSidebarActions) removeFailedTaskAgent(ctx context.Context, taskID string, agent workbench.Session) error {
	if len(agent.Windows) != 1 || len(agent.Windows[0].Panes) != 1 {
		return errors.New("failed task agent identity is incomplete")
	}
	windowID := agent.Windows[0].ID
	paneID := agent.Windows[0].Panes[0].ID
	output, err := exec.CommandContext(ctx, actions.TmuxBinary, "list-panes", "-t", windowID, "-F", "#{pane_id}|#{@fx_task_id}").CombinedOutput()
	if err != nil {
		message := commandOutput(output, err)
		if strings.Contains(message, "can't find window") || strings.Contains(message, "can't find pane") || strings.Contains(message, "no server running") {
			return nil
		}
		return fmt.Errorf("verify failed task window %s: %s", windowID, message)
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) != 1 || (lines[0] != paneID+"|"+taskID && lines[0] != paneID+"|") {
		return fmt.Errorf("window %s no longer contains only created pane %s", windowID, paneID)
	}
	if output, err := exec.CommandContext(ctx, actions.TmuxBinary, "kill-window", "-t", windowID).CombinedOutput(); err != nil {
		return fmt.Errorf("kill window %s: %s", windowID, commandOutput(output, err))
	}
	return nil
}

func (actions *CommandSidebarActions) tagTaskAgent(ctx context.Context, task workload.Entity, windowID, paneID, path string) error {
	profile, err := profiles.Load("coding")
	if err != nil {
		return fmt.Errorf("load coding agent profile: %w", err)
	}
	options := [][2]string{
		{"@fx_task_role", "task"},
		{"@fx_task_id", task.ID},
		{"@fx_repository", task.Repository},
		{"@fx_worktree", path},
		{"@fx_branch", task.Branch},
		{"@fx_agent_profile", profile.ID},
		{"@fx_agent_profile_digest", profile.Digest},
	}
	if task.FXSessionID != "" {
		options = append(options, [2]string{"@fx_session_id", task.FXSessionID})
	}
	for _, option := range options {
		for _, scope := range []struct {
			flag   string
			target string
		}{{"-w", windowID}, {"-p", paneID}} {
			if output, err := exec.CommandContext(ctx, actions.TmuxBinary, "set-option", scope.flag, "-t", scope.target, option[0], option[1]).CombinedOutput(); err != nil {
				return fmt.Errorf("tag task agent: %s", commandOutput(output, err))
			}
		}
	}
	return nil
}

func (actions *CommandSidebarActions) publishTaskAgentSession(ctx context.Context, task workload.Entity, windowID, paneID, fxSessionID string, persist bool) error {
	for _, scope := range []struct {
		flag   string
		target string
	}{{"-w", windowID}, {"-p", paneID}} {
		if output, err := exec.CommandContext(ctx, actions.TmuxBinary, "set-option", scope.flag, "-t", scope.target, "@fx_session_id", fxSessionID).CombinedOutput(); err != nil {
			return fmt.Errorf("tag task agent session: %s", commandOutput(output, err))
		}
	}
	if !persist {
		return nil
	}
	return actions.persistTaskAgentSession(task, fxSessionID)
}

func (actions *CommandSidebarActions) persistTaskAgentSession(task workload.Entity, fxSessionID string) error {
	if actions.Store == nil {
		return errors.New("persist task fx session: workload store is unavailable")
	}
	if err := actions.Store.SetTaskSession(task, fxSessionID); err != nil {
		return fmt.Errorf("persist task fx session: %w", err)
	}
	return nil
}

func (actions *CommandSidebarActions) waitForTaskAgent(ctx context.Context, pane, expectedSessionID string) (string, error) {
	for {
		stateOutput, stateErr := exec.CommandContext(ctx, actions.TmuxBinary, "show-options", "-p", "-v", "-t", pane, "@fx_state").CombinedOutput()
		sessionOutput, sessionErr := exec.CommandContext(ctx, actions.TmuxBinary, "show-options", "-p", "-v", "-t", pane, "@fx_session_id").CombinedOutput()
		state := strings.TrimSpace(string(stateOutput))
		sessionID := strings.TrimSpace(string(sessionOutput))
		if stateErr == nil && sessionErr == nil && state == "idle" && sessionID != "" {
			if expectedSessionID == "" || sessionID == expectedSessionID {
				return sessionID, nil
			}
			return "", &taskAgentStartupError{
				pane: pane, terminal: true,
				detail: fmt.Sprintf("resume published fx session %q instead of %q", sessionID, expectedSessionID),
			}
		}
		if ctx.Err() != nil {
			detail := fmt.Sprintf("last state %q, session %q", state, sessionID)
			return "", &taskAgentStartupError{pane: pane, detail: detail, err: ctx.Err()}
		}
		if terminal, detail := actions.taskAgentTerminalState(ctx, pane, state, sessionID, stateErr, sessionErr); terminal {
			return "", &taskAgentStartupError{pane: pane, detail: detail, terminal: true}
		}
		select {
		case <-ctx.Done():
			detail := fmt.Sprintf("last state %q, session %q", state, sessionID)
			return "", &taskAgentStartupError{pane: pane, detail: detail, err: ctx.Err()}
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (actions *CommandSidebarActions) taskAgentTerminalState(ctx context.Context, pane, state, sessionID string, stateErr, sessionErr error) (bool, string) {
	format := "#{pane_id}|#{pane_dead}|#{pane_dead_status}|#{pane_current_command}"
	output, err := exec.CommandContext(ctx, actions.TmuxBinary, "list-panes", "-t", pane, "-F", format).CombinedOutput()
	if err != nil {
		return true, fmt.Sprintf("pane is missing; last state %q, session %q; tmux: %s", state, sessionID, commandOutput(output, err))
	}
	var fields []string
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		candidate := strings.SplitN(line, "|", 4)
		if len(candidate) == 4 && candidate[0] == pane {
			fields = candidate
			break
		}
	}
	if len(fields) == 0 {
		return true, fmt.Sprintf("pane is missing; last state %q, session %q", state, sessionID)
	}
	if fields[1] != "1" {
		return false, ""
	}
	detail := fmt.Sprintf("pane exited with status %s while running %s; last state %q, session %q", fields[2], fields[3], state, sessionID)
	if captured, captureErr := exec.CommandContext(ctx, actions.TmuxBinary, "capture-pane", "-p", "-S", "-200", "-t", pane).CombinedOutput(); captureErr == nil && strings.TrimSpace(string(captured)) != "" {
		detail += "; output: " + strings.TrimSpace(string(captured))
	} else if stateErr != nil || sessionErr != nil {
		detail += fmt.Sprintf("; readiness lookup errors: state=%v session=%v", stateErr, sessionErr)
	}
	return true, detail
}

func (actions *CommandSidebarActions) sendTaskGuidance(ctx context.Context, task workload.Entity, pane string) error {
	profile, err := profiles.Load("coding")
	if err != nil {
		return fmt.Errorf("load coding agent profile: %w", err)
	}
	installOutput, installErr := exec.CommandContext(ctx, actions.TmuxBinary, "show-options", "-p", "-v", "-t", pane, "@fx_install_state").CombinedOutput()
	installState := strings.TrimSpace(string(installOutput))
	if installErr != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("read dependency install state: %w", ctx.Err())
		}
		if installState == "invalid option: @fx_install_state" {
			installState = ""
		} else {
			return fmt.Errorf("read dependency install state: %s", commandOutput(installOutput, installErr))
		}
	}
	dependency := tasklaunch.DependencyAbsent
	switch installState {
	case "":
	case "running":
		dependency = tasklaunch.DependencyRunning
	case "complete":
		dependency = tasklaunch.DependencyComplete
	case "failed":
		dependency = tasklaunch.DependencyFailed
	default:
		return fmt.Errorf("read dependency install state: unexpected value %q", installState)
	}
	pause := actions.Pause
	if pause == nil {
		pause = promptdelivery.Pause
	}
	title := []rune(strings.TrimSpace(task.Title))
	if len(title) > 60 {
		title = title[:60]
	}
	if len(title) > 0 {
		if output, err := exec.CommandContext(ctx, actions.TmuxBinary, "send-keys", "-l", "-t", pane, "/rename "+string(title)).CombinedOutput(); err != nil {
			return fmt.Errorf("rename task agent: %s", strings.TrimSpace(string(output)))
		}
		if output, err := exec.CommandContext(ctx, actions.TmuxBinary, "send-keys", "-t", pane, "Enter").CombinedOutput(); err != nil {
			return fmt.Errorf("rename task agent: %s", strings.TrimSpace(string(output)))
		}
		if err := pause(ctx, 100*time.Millisecond); err != nil {
			return fmt.Errorf("rename task agent: pause before guidance: %w", err)
		}
	}
	guidance := profile.Text + "\nResume this task with the latest Workbench guidance.\n\n" + tasklaunch.WorkerGuidance(task.Project, task.ID, task.SourcePath, dependency)
	buffer := fmt.Sprintf("fx-resume-guidance-%d", os.Getpid())
	if err := promptdelivery.Paste(ctx, promptdelivery.Exec(actions.TmuxBinary), buffer, pane, []byte(guidance)); err != nil {
		return fmt.Errorf("refresh task guidance: %w", err)
	}
	if err := pause(ctx, promptdelivery.SubmitDelay); err != nil {
		return fmt.Errorf("refresh task guidance: pause before submit: %w", err)
	}
	if output, err := exec.CommandContext(ctx, actions.TmuxBinary, "send-keys", "-t", pane, "Enter").CombinedOutput(); err != nil {
		return fmt.Errorf("refresh task guidance: %s", strings.TrimSpace(string(output)))
	}
	return nil
}

func resolveTaskReviewPath(ctx context.Context, gitBinary, repository, branch string) (string, error) {
	if repository == "" || branch == "" {
		return "", errors.New("offline self-review requires a task repository and branch")
	}
	output, err := exec.CommandContext(ctx, gitBinary, "-C", repository, "worktree", "list", "--porcelain").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("resolve task worktree: %s", strings.TrimSpace(string(output)))
	}
	wantedBranch := "refs/heads/" + branch
	for _, record := range strings.Split(strings.TrimSpace(string(output)), "\n\n") {
		path := ""
		recordBranch := ""
		for _, line := range strings.Split(record, "\n") {
			switch {
			case strings.HasPrefix(line, "worktree "):
				path = strings.TrimPrefix(line, "worktree ")
			case strings.HasPrefix(line, "branch "):
				recordBranch = strings.TrimPrefix(line, "branch ")
			}
		}
		if path != "" && recordBranch == wantedBranch {
			info, statErr := os.Stat(path)
			if statErr != nil {
				return "", fmt.Errorf("task worktree for branch %s is unusable: %s: %w", branch, path, statErr)
			}
			if !info.IsDir() {
				return "", fmt.Errorf("task worktree for branch %s is not a directory: %s", branch, path)
			}
			return path, nil
		}
	}
	return "", fmt.Errorf("no usable worktree found for task branch %s in repository %s", branch, repository)
}

var reviewSummaryPattern = regexp.MustCompile(`tuicr-summary: reviewed ([0-9]+)/([0-9]+) files, ([0-9]+) comments? added`)

func taskReviewScope(ctx context.Context, directory, gitBinary string) ([]string, error) {
	git := func(args ...string) ([]byte, error) {
		command := exec.CommandContext(ctx, gitBinary, args...)
		command.Dir = directory
		return command.CombinedOutput()
	}
	mergeBaseOutput, err := git("merge-base", "main", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("find merge base with main: %s", commandOutput(mergeBaseOutput, err))
	}
	mergeBase := strings.TrimSpace(string(mergeBaseOutput))
	statusOutput, err := git("status", "--porcelain", "--untracked-files=no")
	if err != nil {
		return nil, fmt.Errorf("inspect working-tree changes: %s", commandOutput(statusOutput, err))
	}
	headOutput, err := git("rev-parse", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("resolve HEAD: %s", commandOutput(headOutput, err))
	}
	if mergeBase == strings.TrimSpace(string(headOutput)) {
		if len(statusOutput) == 0 {
			return nil, errors.New("there are no committed or working-tree changes to review")
		}
		return []string{"--working-tree"}, nil
	}
	return []string{"--revisions", mergeBase + "..HEAD", "--working-tree"}, nil
}

func RunReviewRelay(args []string, stderr io.Writer) error {
	return runReviewRelay(args, stderr, promptdelivery.Pause)
}

func runReviewRelay(args []string, stderr io.Writer, pause func(context.Context, time.Duration) error) error {
	flags := flag.NewFlagSet("review-relay", flag.ContinueOnError)
	flags.SetOutput(stderr)
	targetPane := flags.String("target-pane", "", "exact fx agent pane")
	taskID := flags.String("task-id", "", "task id")
	tmuxBinary := flags.String("tmux", "tmux", "tmux executable")
	if err := flags.Parse(args); err != nil {
		return err
	}
	commandArgs := flags.Args()
	if *targetPane == "" || *taskID == "" || len(commandArgs) == 0 {
		return errors.New("review relay requires --target-pane, --task-id, and a tuicr command after --")
	}
	gitBinary := os.Getenv("FX_WORKBENCH_GIT_BIN")
	if gitBinary == "" {
		gitBinary = "git"
	}
	scope, err := taskReviewScope(context.Background(), ".", gitBinary)
	if err != nil {
		return err
	}
	commandArgs = append(commandArgs, scope...)
	commandArgs = append(commandArgs, "--stdout")
	command := exec.Command(commandArgs[0], commandArgs[1:]...)
	command.Stdin = os.Stdin
	var review, summary strings.Builder
	command.Stdout = &review
	command.Stderr = io.MultiWriter(stderr, &summary)
	if err := command.Run(); err != nil {
		return fmt.Errorf("run tuicr: %w", err)
	}
	content := strings.TrimSpace(review.String())
	prompt := ""
	if content != "" {
		prompt = fmt.Sprintf("Self-review feedback for task %s:\n\n%s\n\nAddress every actionable comment, run focused validation, and update the task packet with the outcome. Keep the task in self-review while addressing feedback. If explicitly requested to create a pull request, create it ready for review rather than as a draft. After PR creation succeeds, immediately run `fx-workbench status --set in-review %s`, ask the user which reviewer to request, and add that reviewer without guessing.", *taskID, content, *taskID)
	} else if reviewed, ok := completedEmptyReview(summary.String()); ok {
		prompt = fmt.Sprintf("Self-review completed for task %s with no comments. All %d changed files were marked reviewed. No code changes were requested. Confirm focused validation still passes, record the clean review outcome in the task packet, commit the completed work, push the task branch, and open a ready-for-review pull request rather than a draft. After PR creation succeeds, immediately run `fx-workbench status --set in-review %s`, ask the user which reviewer to request, and add that reviewer without guessing.", *taskID, reviewed, *taskID)
	} else {
		return nil
	}

	buffer := fmt.Sprintf("fx-review-%d", os.Getpid())
	if err := promptdelivery.Paste(context.Background(), promptdelivery.Exec(*tmuxBinary), buffer, *targetPane, []byte(prompt)); err != nil {
		return fmt.Errorf("deliver review to agent: %w", err)
	}
	if err := pause(context.Background(), promptdelivery.SubmitDelay); err != nil {
		return fmt.Errorf("deliver review to agent: pause before submit: %w", err)
	}
	if output, err := exec.Command(*tmuxBinary, "send-keys", "-t", *targetPane, "Enter").CombinedOutput(); err != nil {
		return fmt.Errorf("deliver review to agent: %s", strings.TrimSpace(string(output)))
	}
	return nil
}

func completedEmptyReview(output string) (int, bool) {
	matches := reviewSummaryPattern.FindStringSubmatch(output)
	if len(matches) != 4 {
		return 0, false
	}
	reviewed, reviewedErr := strconv.Atoi(matches[1])
	total, totalErr := strconv.Atoi(matches[2])
	comments, commentsErr := strconv.Atoi(matches[3])
	return reviewed, reviewedErr == nil && totalErr == nil && commentsErr == nil && total > 0 && reviewed == total && comments == 0
}

func (actions *CommandSidebarActions) OpenPullRequest(ctx context.Context, task workload.Entity, restoreAgent bool) error {
	var pullRequestErr error
	if task.Branch == "" || task.Repository == "" {
		pullRequestErr = errors.New("task branch and repository are required to open a pull request")
	} else {
		command := exec.CommandContext(ctx, actions.GHBinary, "pr", "view", task.Branch, "--web")
		command.Dir = task.Repository
		if output, err := command.CombinedOutput(); err != nil {
			pullRequestErr = fmt.Errorf("open pull request: %s", commandOutput(output, err))
		}
	}
	if !restoreAgent {
		return pullRequestErr
	}

	restoreErr := actions.Resume(ctx, task)
	switch {
	case pullRequestErr == nil && restoreErr != nil:
		return fmt.Errorf("pull request opened; restore agent: %w", restoreErr)
	case pullRequestErr != nil && restoreErr == nil:
		return fmt.Errorf("agent restored; %w", pullRequestErr)
	case pullRequestErr != nil && restoreErr != nil:
		return fmt.Errorf("%v; restore agent: %w", pullRequestErr, restoreErr)
	default:
		return nil
	}
}

type ManagedLauncher struct {
	Service *tasklaunch.Service
	Store   *workload.Store
}

func (launcher *ManagedLauncher) Launch(ctx context.Context, task workload.Entity, branch string, report func(tasklaunch.Progress)) (tasklaunch.Result, error) {
	result, err := launcher.Service.Launch(ctx, task, branch, report)
	if err != nil {
		return result, err
	}
	if err := launcher.Store.SetTaskLaunch(task, result.FXSessionID); err != nil {
		return result, fmt.Errorf("task started but launch metadata update failed: %w", err)
	}
	return result, nil
}

func NewDefaultModel(project string, runtime Runtime) Model {
	store := workload.NewStore()
	if project == "" {
		if resolution, err := store.ResolveProject("", ""); err == nil {
			project = resolution.Project.ID
		}
	}
	launcher := &ManagedLauncher{Service: tasklaunch.NewService(tasklaunch.NewRuntime()), Store: store}
	return NewModelWithLauncher(store, NewCommandActions(), runtime, project, launcher)
}

func Run(args []string, stderr io.Writer) error {
	flags := flag.NewFlagSet("tasks", flag.ContinueOnError)
	flags.SetOutput(stderr)
	project := flags.String("project", "", "show only one project")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", flags.Args())
	}
	model := NewDefaultModel(*project, workbench.NewTmuxSource(""))
	_, err := tea.NewProgram(model, tea.WithFPS(120)).Run()
	return err
}

func RunSidebar(args []string, stderr io.Writer) error {
	flags := flag.NewFlagSet("sidebar", flag.ContinueOnError)
	flags.SetOutput(stderr)
	project := flags.String("project", "", "project id")
	workstream := flags.String("workstream", "", "workstream id")
	refresh := flags.Duration("refresh", defaultSidebarRefreshInterval, "automatic refresh interval")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", flags.Args())
	}
	if *project == "" || *workstream == "" {
		return errors.New("--project and --workstream are required")
	}
	if *refresh < 250*time.Millisecond || *refresh > time.Minute {
		return errors.New("--refresh must be between 250ms and 1m")
	}
	runtime := workbench.NewTmuxSource("")
	model := NewSidebarModel(workload.NewStore(), runtime, *project, *workstream, *refresh).WithActions(NewCommandSidebarActions(runtime))
	_, err := tea.NewProgram(model, tea.WithFPS(30), tea.WithAltScreen()).Run()
	return err
}
