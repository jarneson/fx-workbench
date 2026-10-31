package planner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jarneson/fx-workbench/internal/promptdelivery"
	runtimeworkbench "github.com/jarneson/fx-workbench/internal/workbench"
)

const (
	formatSeparator = "\x1f"
	outputSeparator = `\037`
)

type CommandRunner interface {
	Run(context.Context, io.Reader, string, ...string) ([]byte, error)
}

type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, stdin io.Reader, name string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, args...)
	command.Stdin = stdin
	return command.CombinedOutput()
}

type TmuxRuntime struct {
	Binary         string
	FXCommand      string
	SidebarCommand string
	Client         string
	InsideTmux     bool
	Runner         CommandRunner
	Sleep          func(time.Duration)
	Pause          func(context.Context, time.Duration) error
}

type tmuxSession struct {
	ID, Name, Role, Project, Workstream, Repository string
}

type plannerPane struct {
	WindowID, WindowName, PaneID      string
	CurrentPath, Command              string
	State, Title, TaskID, FXSessionID string
	Role, Project, Workstream         string
	Dead, Tagged                      bool
}

func NewTmuxRuntime(binary, fxCommand string) *TmuxRuntime {
	if binary == "" {
		binary = os.Getenv("FX_WORKBENCH_TMUX_BIN")
	}
	if binary == "" {
		binary = "tmux"
	}
	if fxCommand == "" {
		fxCommand = os.Getenv("FX_WORKBENCH_FX_TMUX")
	}
	if fxCommand == "" {
		fxCommand = "fx-tmux"
	}
	sidebarCommand := os.Getenv("FX_WORKBENCH_COMMAND")
	if sidebarCommand == "" {
		sidebarCommand = "fx-workbench"
	}
	client := os.Getenv("FX_WORKBENCH_CLIENT")
	if strings.Contains(client, "#{") {
		client = ""
	}
	return &TmuxRuntime{
		Binary: binary, FXCommand: fxCommand, SidebarCommand: sidebarCommand, Client: client,
		InsideTmux: os.Getenv("TMUX") != "", Runner: ExecRunner{}, Sleep: time.Sleep, Pause: promptdelivery.Pause,
	}
}

func (runtime *TmuxRuntime) Ensure(ctx context.Context, plan Plan) (LaunchResult, error) {
	identity, err := runtimeworkbench.NewWorkstreamIdentity(plan.Project.ID, plan.Workstream.ID, plan.RootRepository)
	if err != nil {
		return LaunchResult{}, err
	}
	sessions, err := runtime.sessions(ctx)
	if err != nil {
		return LaunchResult{}, err
	}
	session, err := findWorkstreamSession(sessions, identity)
	if err != nil {
		return LaunchResult{}, err
	}
	migrating, sourceSession, err := runtime.findTaggedPlanner(ctx, sessions, plan)
	if err != nil {
		return LaunchResult{}, err
	}

	createdSession := false
	bootstrapWindow := ""
	if session.ID == "" {
		output, commandErr := runtime.run(ctx, "new-session", "-d", "-P", "-F", "#{session_id}"+formatSeparator+"#{window_id}", "-s", identity.SessionName, "-c", identity.Repository, "-n", "bootstrap")
		if commandErr != nil {
			return LaunchResult{}, fmt.Errorf("tmux new workstream session: %s", commandError(output, commandErr))
		}
		fields := strings.Split(strings.TrimSpace(string(output)), outputSeparator)
		if len(fields) != 2 {
			return LaunchResult{}, fmt.Errorf("tmux new-session returned malformed identity: %q", strings.TrimSpace(string(output)))
		}
		session = tmuxSession{ID: fields[0], Name: identity.SessionName, Role: runtimeworkbench.RoleWorkstream, Project: identity.Project, Workstream: identity.Workstream, Repository: identity.Repository}
		bootstrapWindow = fields[1]
		createdSession = true
	}
	for _, option := range identity.SessionOptions() {
		if err := runtime.setOption(ctx, session.ID, option[0], option[1]); err != nil {
			return LaunchResult{}, err
		}
	}
	if err := runtime.setOption(ctx, session.ID, "renumber-windows", "on"); err != nil {
		return LaunchResult{}, err
	}

	adoptedPlanner := false
	if migrating.WindowID != "" && sourceSession != session.ID {
		if output, moveErr := runtime.run(ctx, "move-window", "-s", migrating.WindowID, "-t", session.ID+":"); moveErr != nil {
			return LaunchResult{}, fmt.Errorf("move planner window into workstream session: %s", commandError(output, moveErr))
		}
		adoptedPlanner = true
	}

	pane, inferredPlanner, findErr := runtime.findPlanner(ctx, session.ID, plan.Project.ID, plan.Workstream.ID, plan.Workstream.PlannerSessionID, plan.RootRepository)
	createdPlanner := false
	if findErr != nil && !errors.Is(findErr, ErrNoPlanner) {
		return LaunchResult{}, findErr
	}
	if errors.Is(findErr, ErrNoPlanner) {
		arguments := []string{"new-window", "-d", "-P", "-F", "#{window_id}" + formatSeparator + "#{pane_id}", "-t", session.ID + ":", "-n", plan.WindowName, "-c", plan.RootRepository, runtime.FXCommand}
		if plan.Workstream.PlannerSessionID != "" {
			arguments = append(arguments, "--resume", plan.Workstream.PlannerSessionID)
		}
		output, commandErr := runtime.run(ctx, arguments...)
		if commandErr != nil {
			return LaunchResult{}, fmt.Errorf("tmux new planner window: %s", commandError(output, commandErr))
		}
		fields := strings.Split(strings.TrimSpace(string(output)), outputSeparator)
		if len(fields) != 2 {
			return LaunchResult{}, fmt.Errorf("tmux new-window returned malformed identity: %q", strings.TrimSpace(string(output)))
		}
		pane = plannerPane{WindowID: fields[0], WindowName: plan.WindowName, PaneID: fields[1]}
		createdPlanner = true
		if err := runtime.tagPlannerWindow(ctx, pane.WindowID, plan); err != nil {
			return LaunchResult{}, err
		}
		if err := runtime.waitReady(ctx, pane.PaneID); err != nil {
			return LaunchResult{}, err
		}
		fxSessionID, sessionErr := runtime.waitSessionID(ctx, pane.PaneID)
		if sessionErr != nil {
			return LaunchResult{}, sessionErr
		}
		pane.FXSessionID = fxSessionID
		if plan.Workstream.PlannerSessionID == "" {
			if err := runtime.deliverPrompt(ctx, pane.PaneID, session.Name, plan.Prompt); err != nil {
				return LaunchResult{}, err
			}
		}
		if plan.Workstream.PlannerSessionID != "" && plan.Workstream.PlannerSessionID != pane.FXSessionID {
			return LaunchResult{}, fmt.Errorf("resumed planner reported session %s, expected %s", pane.FXSessionID, plan.Workstream.PlannerSessionID)
		}
		if plan.Workstream.PlannerSessionID != "" && plan.Profile.ID != "" {
			guidance := plan.Profile.Text + "\nResume this planner with the latest Workbench agent profile. Continue the existing workstream conversation; do not replay the original bootstrap."
			if err := runtime.deliverPrompt(ctx, pane.PaneID, session.Name, guidance); err != nil {
				return LaunchResult{}, err
			}
		}
	} else if inferredPlanner {
		adoptedPlanner = true
		if err := runtime.tagPlannerWindow(ctx, pane.WindowID, plan); err != nil {
			return LaunchResult{}, err
		}
	}
	if bootstrapWindow != "" {
		if output, killErr := runtime.run(ctx, "kill-window", "-t", bootstrapWindow); killErr != nil {
			return LaunchResult{}, fmt.Errorf("remove workstream bootstrap window: %s", commandError(output, killErr))
		}
	}
	if err := runtime.ensurePlannerFirst(ctx, session.ID, pane.WindowID); err != nil {
		return LaunchResult{}, err
	}
	if err := runtime.tagPlannerPane(ctx, pane.PaneID, "planner", plan.Project.ID, plan.Workstream.ID); err != nil {
		return LaunchResult{}, err
	}
	if pane.FXSessionID == "" {
		fxSessionID, sessionErr := runtime.waitSessionID(ctx, pane.PaneID)
		if sessionErr != nil {
			return LaunchResult{}, sessionErr
		}
		pane.FXSessionID = fxSessionID
	}
	if err := runtime.ensureSidebar(ctx, pane, plan); err != nil {
		return LaunchResult{}, err
	}
	if err := runtime.focus(ctx, session.ID, pane.WindowID, pane.PaneID); err != nil {
		return LaunchResult{}, err
	}
	return LaunchResult{
		SessionName: session.Name, SessionID: session.ID, WindowName: pane.WindowName,
		WindowID: pane.WindowID, PaneID: pane.PaneID, FXSessionID: pane.FXSessionID,
		CreatedSession: createdSession, CreatedPlanner: createdPlanner, AdoptedPlanner: adoptedPlanner,
	}, nil
}

func (runtime *TmuxRuntime) sessions(ctx context.Context) ([]tmuxSession, error) {
	format := strings.Join([]string{"#{session_id}", "#{session_name}", "#{@fx_workflow_role}", "#{@fx_project_id}", "#{@fx_workstream_id}", "#{@fx_repository}"}, formatSeparator)
	output, err := runtime.run(ctx, "list-sessions", "-F", format)
	if err != nil {
		message := string(output)
		if strings.Contains(message, "no server running") || strings.Contains(message, "failed to connect") || strings.Contains(message, "error connecting") {
			return nil, nil
		}
		return nil, fmt.Errorf("tmux list-sessions: %s", commandError(output, err))
	}
	var sessions []tmuxSession
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Split(line, outputSeparator)
		if len(fields) == 6 {
			sessions = append(sessions, tmuxSession{ID: fields[0], Name: fields[1], Role: fields[2], Project: fields[3], Workstream: fields[4], Repository: fields[5]})
		}
	}
	return sessions, nil
}

func findWorkstreamSession(sessions []tmuxSession, identity runtimeworkbench.WorkstreamIdentity) (tmuxSession, error) {
	var matches []tmuxSession
	for _, candidate := range sessions {
		exact := candidate.Role == runtimeworkbench.RoleWorkstream && candidate.Project == identity.Project && candidate.Workstream == identity.Workstream && canonicalPath(candidate.Repository) == identity.Repository
		if exact {
			matches = append(matches, candidate)
			continue
		}
		if candidate.Name == identity.SessionName {
			return tmuxSession{}, fmt.Errorf("tmux session %s exists but does not match workstream identity %s/%s in %s", identity.SessionName, identity.Project, identity.Workstream, identity.Repository)
		}
	}
	if len(matches) == 0 {
		return tmuxSession{}, nil
	}
	if len(matches) > 1 {
		names := make([]string, len(matches))
		for index, candidate := range matches {
			names[index] = candidate.Name
		}
		sort.Strings(names)
		return tmuxSession{}, fmt.Errorf("multiple tmux sessions match workstream %s/%s in %s: %s", identity.Project, identity.Workstream, identity.Repository, strings.Join(names, ", "))
	}
	if matches[0].Name != identity.SessionName {
		return tmuxSession{}, fmt.Errorf("workstream session %s uses legacy name %q; expected %q and will not be merged automatically", matches[0].ID, matches[0].Name, identity.SessionName)
	}
	return matches[0], nil
}

func (runtime *TmuxRuntime) findTaggedPlanner(ctx context.Context, sessions []tmuxSession, plan Plan) (plannerPane, string, error) {
	format := strings.Join([]string{
		"#{session_id}", "#{window_id}", "#{window_name}", "#{@fx_planner}", "#{@fx_planner_project}", "#{@fx_planner_workstream}",
		"#{pane_id}", "#{pane_dead}", "#{pane_current_path}", "#{pane_current_command}", "#{@fx_state}", "#{@fx_session_title}",
		"#{@fx_task_id}", "#{@fx_session_id}", "#{@fx_planner_role}", "#{@fx_planner_pane_project}", "#{@fx_planner_pane_workstream}",
	}, formatSeparator)
	output, err := runtime.run(ctx, "list-panes", "-a", "-F", format)
	if err != nil {
		message := string(output)
		if strings.Contains(message, "no server running") || strings.Contains(message, "failed to connect") || strings.Contains(message, "error connecting") {
			return plannerPane{}, "", nil
		}
		return plannerPane{}, "", fmt.Errorf("tmux list tagged planners: %s", commandError(output, err))
	}
	byID := make(map[string]tmuxSession, len(sessions))
	for _, session := range sessions {
		byID[session.ID] = session
	}
	type match struct {
		pane      plannerPane
		sessionID string
	}
	var matches []match
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		fields := strings.Split(line, outputSeparator)
		if len(fields) != 17 || fields[7] != "0" || fields[3] != "1" || fields[4] != plan.Project.ID || fields[5] != plan.Workstream.ID {
			continue
		}
		if fields[14] == "sidebar" {
			continue
		}
		if fields[14] != "" && (fields[14] != "planner" || fields[15] != plan.Project.ID || fields[16] != plan.Workstream.ID) {
			continue
		}
		source := byID[fields[0]]
		if source.ID == "" || canonicalPath(source.Repository) != canonicalPath(plan.RootRepository) || (source.Role != runtimeworkbench.RoleRoot && source.Role != runtimeworkbench.RoleWorkstream) {
			continue
		}
		matches = append(matches, match{pane: plannerPane{
			WindowID: fields[1], WindowName: fields[2], PaneID: fields[6], CurrentPath: fields[8], Command: fields[9],
			State: fields[10], Title: fields[11], TaskID: fields[12], FXSessionID: fields[13], Role: fields[14], Project: fields[15], Workstream: fields[16], Tagged: true,
		}, sessionID: fields[0]})
	}
	if len(matches) == 0 {
		return plannerPane{}, "", nil
	}
	if len(matches) > 1 {
		return plannerPane{}, "", fmt.Errorf("multiple exact tagged planner panes match %s/%s in %s; refusing automatic migration", plan.Project.ID, plan.Workstream.ID, plan.RootRepository)
	}
	return matches[0].pane, matches[0].sessionID, nil
}

func (runtime *TmuxRuntime) tagPlannerWindow(ctx context.Context, window string, plan Plan) error {
	for _, option := range [][2]string{
		{"@fx_planner", "1"},
		{"@fx_planner_project", plan.Project.ID},
		{"@fx_planner_workstream", plan.Workstream.ID},
		{"@fx_agent_profile", plan.Profile.ID},
		{"@fx_agent_profile_digest", plan.Profile.Digest},
	} {
		if err := runtime.setWindowOption(ctx, window, option[0], option[1]); err != nil {
			return err
		}
	}
	return nil
}

func (runtime *TmuxRuntime) ensurePlannerFirst(ctx context.Context, sessionID, plannerWindow string) error {
	format := strings.Join([]string{"#{window_id}", "#{window_index}"}, formatSeparator)
	output, err := runtime.run(ctx, "list-windows", "-t", sessionID, "-F", format)
	if err != nil {
		return fmt.Errorf("tmux list workstream windows: %s", commandError(output, err))
	}
	firstWindow, firstIndex := "", 0
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		fields := strings.Split(line, outputSeparator)
		if len(fields) != 2 {
			continue
		}
		index, parseErr := strconv.Atoi(fields[1])
		if parseErr == nil && (firstWindow == "" || index < firstIndex) {
			firstWindow, firstIndex = fields[0], index
		}
	}
	if firstWindow == "" {
		return fmt.Errorf("workstream session %s has no windows", sessionID)
	}
	if firstWindow != plannerWindow {
		if output, swapErr := runtime.run(ctx, "swap-window", "-s", plannerWindow, "-t", firstWindow); swapErr != nil {
			return fmt.Errorf("move planner to first workstream window: %s", commandError(output, swapErr))
		}
	}
	if output, renumberErr := runtime.run(ctx, "move-window", "-r", "-t", sessionID); renumberErr != nil {
		return fmt.Errorf("renumber workstream windows: %s", commandError(output, renumberErr))
	}
	return nil
}

func (runtime *TmuxRuntime) findPlanner(ctx context.Context, sessionID, project, workstream, expectedFXSessionID, root string) (plannerPane, bool, error) {
	format := strings.Join([]string{
		"#{window_id}", "#{window_name}", "#{@fx_planner}", "#{@fx_planner_project}", "#{@fx_planner_workstream}",
		"#{pane_id}", "#{pane_dead}", "#{pane_current_path}", "#{pane_current_command}",
		"#{@fx_state}", "#{@fx_session_title}", "#{@fx_task_id}", "#{@fx_session_id}",
		"#{@fx_planner_role}", "#{@fx_planner_pane_project}", "#{@fx_planner_pane_workstream}",
	}, formatSeparator)
	output, err := runtime.run(ctx, "list-panes", "-s", "-t", sessionID, "-F", format)
	if err != nil {
		return plannerPane{}, false, fmt.Errorf("tmux list-panes: %s", commandError(output, err))
	}
	var exact, inferred []plannerPane
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		fields := strings.Split(line, outputSeparator)
		if len(fields) != 16 || fields[6] != "0" {
			continue
		}
		exactWindow := fields[2] == "1" && fields[3] == project && fields[4] == workstream
		pane := plannerPane{
			WindowID: fields[0], WindowName: fields[1], PaneID: fields[5],
			CurrentPath: fields[7], Command: fields[8], State: fields[9], Title: fields[10], TaskID: fields[11], FXSessionID: fields[12],
			Role: fields[13], Project: fields[14], Workstream: fields[15], Tagged: exactWindow,
		}
		if exactWindow {
			if pane.Role == "sidebar" {
				continue
			}
			if pane.Role != "" && (pane.Role != "planner" || pane.Project != project || pane.Workstream != workstream) {
				continue
			}
			exact = append(exact, pane)
			continue
		}
		if fields[2] == "1" && (fields[3] != project || fields[4] != "") {
			continue
		}
		label := strings.ToLower(pane.WindowName + " " + pane.Title)
		sessionMatch := expectedFXSessionID != "" && pane.FXSessionID == expectedFXSessionID
		planningLabel := strings.Contains(label, "plan")
		if canonicalPath(pane.CurrentPath) == canonicalPath(root) && pane.TaskID == "" && pane.State != "" && (sessionMatch || planningLabel) {
			inferred = append(inferred, pane)
		}
	}
	if len(exact) > 1 {
		return plannerPane{}, false, fmt.Errorf("planner window for %s/%s has multiple candidate agent panes", project, workstream)
	}
	if len(exact) == 1 {
		return exact[0], false, nil
	}
	if len(inferred) == 0 {
		return plannerPane{}, false, ErrNoPlanner
	}
	if len(inferred) > 1 {
		return plannerPane{}, false, fmt.Errorf("multiple legacy planner panes match %s/%s; tag the intended planner before retrying", project, workstream)
	}
	return inferred[0], true, nil
}

func (runtime *TmuxRuntime) tagPlannerPane(ctx context.Context, pane, role, project, workstream string) error {
	for _, option := range [][2]string{
		{"@fx_planner_role", role},
		{"@fx_planner_pane_project", project},
		{"@fx_planner_pane_workstream", workstream},
	} {
		if err := runtime.setPaneOption(ctx, pane, option[0], option[1]); err != nil {
			return err
		}
	}
	return nil
}

func (runtime *TmuxRuntime) ensureSidebar(ctx context.Context, planner plannerPane, plan Plan) error {
	format := strings.Join([]string{"#{pane_id}", "#{pane_dead}", "#{@fx_planner_role}", "#{@fx_planner_pane_project}", "#{@fx_planner_pane_workstream}"}, formatSeparator)
	output, err := runtime.run(ctx, "list-panes", "-t", planner.WindowID, "-F", format)
	if err != nil {
		return fmt.Errorf("tmux list planner panes: %s", commandError(output, err))
	}
	var sidebarIDs, unknown []string
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		fields := strings.Split(line, outputSeparator)
		if len(fields) != 5 || fields[1] != "0" || fields[0] == planner.PaneID {
			continue
		}
		if fields[2] == "sidebar" && fields[3] == plan.Project.ID && fields[4] == plan.Workstream.ID {
			sidebarIDs = append(sidebarIDs, fields[0])
			continue
		}
		unknown = append(unknown, fields[0])
	}
	if len(sidebarIDs) > 1 {
		return fmt.Errorf("planner window %s has multiple sidebars for %s/%s", planner.WindowID, plan.Project.ID, plan.Workstream.ID)
	}
	if len(sidebarIDs) == 1 {
		return runtime.setPaneTitle(ctx, sidebarIDs[0], plan.Project.ID)
	}
	if len(unknown) > 0 {
		return fmt.Errorf("planner window %s has unrecognized panes; refusing to add a sidebar", planner.WindowID)
	}
	width := runtime.sidebarWidth(ctx, planner.WindowID)
	command := runtime.SidebarCommand
	if command == "" {
		command = "fx-workbench"
	}
	arguments := []string{
		"split-window", "-b", "-h", "-l", strconv.Itoa(width), "-d", "-P", "-F", "#{pane_id}",
		"-t", planner.PaneID, "-c", plan.RootRepository, command, "sidebar", "--project", plan.Project.ID, "--workstream", plan.Workstream.ID,
	}
	output, err = runtime.run(ctx, arguments...)
	if err != nil {
		return fmt.Errorf("tmux split sidebar: %s", commandError(output, err))
	}
	sidebar := strings.TrimSpace(string(output))
	if sidebar == "" {
		return errors.New("tmux split sidebar returned no pane id")
	}
	if err := runtime.tagPlannerPane(ctx, sidebar, "sidebar", plan.Project.ID, plan.Workstream.ID); err != nil {
		return err
	}
	return runtime.setPaneTitle(ctx, sidebar, plan.Project.ID)
}

func (runtime *TmuxRuntime) setPaneTitle(ctx context.Context, pane, title string) error {
	if output, err := runtime.run(ctx, "select-pane", "-t", pane, "-T", title); err != nil {
		return fmt.Errorf("tmux set pane title: %s", commandError(output, err))
	}
	return nil
}

func (runtime *TmuxRuntime) sidebarWidth(ctx context.Context, window string) int {
	output, err := runtime.run(ctx, "display-message", "-p", "-t", window, "#{window_width}")
	width, parseErr := strconv.Atoi(strings.TrimSpace(string(output)))
	if err != nil || parseErr != nil || width <= 0 {
		width = 120
	}
	return boundedSidebarWidth(width)
}

func boundedSidebarWidth(width int) int {
	if width < 26 {
		return max(1, (width-1)/2)
	}
	sidebar := width * 30 / 100
	if sidebar < 18 {
		sidebar = 18
	}
	if sidebar > 34 {
		sidebar = 34
	}
	if maximum := width - 25; sidebar > maximum {
		sidebar = maximum
	}
	return sidebar
}

func (runtime *TmuxRuntime) waitReady(ctx context.Context, pane string) error {
	for range 300 {
		output, _ := runtime.run(ctx, "show-options", "-p", "-v", "-t", pane, "@fx_state")
		if strings.TrimSpace(string(output)) == "idle" {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			runtime.Sleep(50 * time.Millisecond)
		}
	}
	return fmt.Errorf("fx planner did not become ready in pane %s", pane)
}

func (runtime *TmuxRuntime) waitSessionID(ctx context.Context, pane string) (string, error) {
	for range 300 {
		output, err := runtime.run(ctx, "show-options", "-p", "-v", "-t", pane, "@fx_session_id")
		if err == nil {
			if sessionID := strings.TrimSpace(string(output)); sessionID != "" {
				return sessionID, nil
			}
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
			runtime.Sleep(50 * time.Millisecond)
		}
	}
	return "", fmt.Errorf("fx planner did not report a session id in pane %s", pane)
}

func (runtime *TmuxRuntime) deliverPrompt(ctx context.Context, pane, sessionName, prompt string) error {
	buffer := "fx-plan-" + safeComponent(sessionName) + "-" + strconv.Itoa(os.Getpid())
	if err := promptdelivery.Paste(ctx, func(ctx context.Context, stdin io.Reader, args ...string) ([]byte, error) {
		return runtime.runInput(ctx, stdin, args...)
	}, buffer, pane, []byte(prompt)); err != nil {
		return err
	}
	pause := runtime.Pause
	if pause == nil {
		pause = promptdelivery.Pause
	}
	if err := pause(ctx, promptdelivery.SubmitDelay); err != nil {
		return fmt.Errorf("pause before submitting prompt: %w", err)
	}
	if output, err := runtime.run(ctx, "send-keys", "-t", pane, "Enter"); err != nil {
		return fmt.Errorf("tmux send-keys: %s", commandError(output, err))
	}
	return nil
}

func (runtime *TmuxRuntime) focus(ctx context.Context, session, window, pane string) error {
	if runtime.InsideTmux || runtime.Client != "" {
		args := []string{"switch-client"}
		if runtime.Client != "" {
			args = append(args, "-c", runtime.Client)
		}
		args = append(args, "-t", session)
		if output, err := runtime.run(ctx, args...); err != nil {
			return fmt.Errorf("tmux switch-client: %s", commandError(output, err))
		}
	}
	if output, err := runtime.run(ctx, "select-window", "-t", window); err != nil {
		return fmt.Errorf("tmux select-window: %s", commandError(output, err))
	}
	if output, err := runtime.run(ctx, "select-pane", "-t", pane); err != nil {
		return fmt.Errorf("tmux select-pane: %s", commandError(output, err))
	}
	return nil
}

func (runtime *TmuxRuntime) setOption(ctx context.Context, target, option, value string) error {
	output, err := runtime.run(ctx, "set-option", "-t", target, option, value)
	if err != nil {
		return fmt.Errorf("tmux set-option %s: %s", option, commandError(output, err))
	}
	return nil
}

func (runtime *TmuxRuntime) setWindowOption(ctx context.Context, target, option, value string) error {
	output, err := runtime.run(ctx, "set-window-option", "-t", target, option, value)
	if err != nil {
		return fmt.Errorf("tmux set-window-option %s: %s", option, commandError(output, err))
	}
	return nil
}

func (runtime *TmuxRuntime) setPaneOption(ctx context.Context, target, option, value string) error {
	output, err := runtime.run(ctx, "set-option", "-p", "-t", target, option, value)
	if err != nil {
		return fmt.Errorf("tmux set pane option %s: %s", option, commandError(output, err))
	}
	return nil
}

func (runtime *TmuxRuntime) run(ctx context.Context, args ...string) ([]byte, error) {
	return runtime.runInput(ctx, nil, args...)
}

func (runtime *TmuxRuntime) runInput(ctx context.Context, stdin io.Reader, args ...string) ([]byte, error) {
	return runtime.Runner.Run(ctx, stdin, runtime.Binary, args...)
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

func commandError(output []byte, err error) string {
	if message := strings.TrimSpace(string(output)); message != "" {
		return message
	}
	return err.Error()
}
