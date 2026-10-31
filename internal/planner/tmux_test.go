package planner

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/jarneson/fx-workbench/internal/profiles"
	"github.com/jarneson/fx-workbench/internal/promptdelivery"
	runtimeworkbench "github.com/jarneson/fx-workbench/internal/workbench"
	"github.com/jarneson/fx-workbench/internal/workload"
)

type fakeCommandRunner struct {
	commands        [][]string
	events          []string
	sessions        []tmuxSession
	pane            plannerPane
	paneSessionID   string
	paneRole        string
	paneProject     string
	paneWorkstream  string
	reportedSession string
	sessionFailures int
	sidebarPaneID   string
	sidebarRole     string
	sidebarProject  string
	sidebarStream   string
	taskWindowID    string
	prompt          []byte
}

func (runner *fakeCommandRunner) Run(_ context.Context, stdin io.Reader, name string, args ...string) ([]byte, error) {
	command := append([]string{name}, args...)
	runner.commands = append(runner.commands, command)
	runner.events = append(runner.events, args[0])
	if stdin != nil {
		var err error
		runner.prompt, err = io.ReadAll(stdin)
		if err != nil {
			return nil, err
		}
	}
	switch args[0] {
	case "list-sessions":
		var lines []string
		for _, session := range runner.sessions {
			lines = append(lines, strings.Join([]string{session.ID, session.Name, session.Role, session.Project, session.Workstream, session.Repository}, outputSeparator))
		}
		return []byte(strings.Join(lines, "\n")), nil
	case "new-session":
		name := valueAfter(args, "-s")
		repository := valueAfter(args, "-c")
		runner.sessions = append(runner.sessions, tmuxSession{ID: "$new", Name: name, Repository: repository})
		return []byte("$new" + outputSeparator + "@bootstrap\n"), nil
	case "set-option":
		if commandSliceContains([][]string{args}, "-p") {
			target := valueAfter(args, "-t")
			option, value := args[len(args)-2], args[len(args)-1]
			if target == runner.sidebarPaneID {
				switch option {
				case "@fx_planner_role":
					runner.sidebarRole = value
				case "@fx_planner_pane_project":
					runner.sidebarProject = value
				case "@fx_planner_pane_workstream":
					runner.sidebarStream = value
				}
			} else {
				switch option {
				case "@fx_planner_role":
					runner.pane.Role = value
				case "@fx_planner_pane_project":
					runner.pane.Project = value
				case "@fx_planner_pane_workstream":
					runner.pane.Workstream = value
				}
			}
			return nil, nil
		}
		target := valueAfter(args, "-t")
		for index := range runner.sessions {
			if runner.sessions[index].ID != target {
				continue
			}
			option, value := args[len(args)-2], args[len(args)-1]
			switch option {
			case "@fx_workflow_role":
				runner.sessions[index].Role = value
			case "@fx_project_id":
				runner.sessions[index].Project = value
			case "@fx_workstream_id":
				runner.sessions[index].Workstream = value
			case "@fx_repository":
				runner.sessions[index].Repository = value
			}
		}
		return nil, nil
	case "list-panes":
		format := valueAfter(args, "-F")
		if strings.Contains(format, "#{window_index}") {
			sessionID := valueAfter(args, "-t")
			root := ""
			for _, session := range runner.sessions {
				if session.ID == sessionID {
					root = session.Repository
				}
			}
			if root == "" && sessionID == runner.paneSessionID {
				root = runner.pane.CurrentPath
			}
			return []byte(strings.Join([]string{"@terminal", "0", "shell", "%terminal", "0", root}, outputSeparator)), nil
		}
		if commandSliceContains([][]string{args}, "-a") {
			if runner.pane.PaneID == "" {
				return nil, nil
			}
			dead := "0"
			if runner.pane.Dead {
				dead = "1"
			}
			return []byte(strings.Join([]string{
				runner.paneSessionID, runner.pane.WindowID, runner.pane.WindowName,
				runner.paneRole, runner.paneProject, runner.paneWorkstream,
				runner.pane.PaneID, dead, runner.pane.CurrentPath, runner.pane.Command,
				runner.pane.State, runner.pane.Title, runner.pane.TaskID, runner.pane.FXSessionID,
				runner.pane.Role, runner.pane.Project, runner.pane.Workstream,
			}, outputSeparator)), nil
		}
		if strings.Contains(format, "@fx_planner_role") && !strings.Contains(format, "@fx_state") {
			var lines []string
			if runner.pane.PaneID != "" {
				lines = append(lines, strings.Join([]string{runner.pane.PaneID, "0", runner.pane.Role, runner.pane.Project, runner.pane.Workstream}, outputSeparator))
			}
			if runner.sidebarPaneID != "" {
				lines = append(lines, strings.Join([]string{runner.sidebarPaneID, "0", runner.sidebarRole, runner.sidebarProject, runner.sidebarStream}, outputSeparator))
			}
			return []byte(strings.Join(lines, "\n")), nil
		}
		if runner.pane.PaneID == "" || runner.paneSessionID != valueAfter(args, "-t") {
			return nil, nil
		}
		dead := "0"
		if runner.pane.Dead {
			dead = "1"
		}
		return []byte(strings.Join([]string{
			runner.pane.WindowID, runner.pane.WindowName, runner.paneRole, runner.paneProject, runner.paneWorkstream,
			runner.pane.PaneID, dead, runner.pane.CurrentPath, runner.pane.Command,
			runner.pane.State, runner.pane.Title, runner.pane.TaskID, runner.pane.FXSessionID,
			runner.pane.Role, runner.pane.Project, runner.pane.Workstream,
		}, outputSeparator)), nil
	case "list-windows":
		if runner.pane.PaneID == "" {
			return []byte("@bootstrap" + outputSeparator + "0\n"), nil
		}
		if runner.taskWindowID != "" {
			return []byte(runner.taskWindowID + outputSeparator + "0\n" + runner.pane.WindowID + outputSeparator + "1\n"), nil
		}
		return []byte(runner.pane.WindowID + outputSeparator + "0\n"), nil
	case "move-window":
		if valueAfter(args, "-s") == runner.pane.WindowID {
			runner.paneSessionID = strings.TrimSuffix(valueAfter(args, "-t"), ":")
		}
		return nil, nil
	case "new-window":
		runner.paneSessionID = strings.TrimSuffix(valueAfter(args, "-t"), ":")
		runner.pane = plannerPane{WindowID: "@new", WindowName: valueAfter(args, "-n"), PaneID: "%new", CurrentPath: valueAfter(args, "-c"), State: "idle"}
		return []byte("@new" + outputSeparator + "%new\n"), nil
	case "split-window":
		runner.sidebarPaneID = "%sidebar"
		return []byte("%sidebar\n"), nil
	case "set-window-option":
		option, value := args[len(args)-2], args[len(args)-1]
		if option == "@fx_planner" {
			runner.paneRole = value
		}
		if option == "@fx_planner_project" {
			runner.paneProject = value
		}
		if option == "@fx_planner_workstream" {
			runner.paneWorkstream = value
		}
		return nil, nil
	case "show-options":
		if args[len(args)-1] == "@fx_session_id" {
			if runner.sessionFailures > 0 {
				runner.sessionFailures--
				return []byte("invalid option: @fx_session_id\n"), errors.New("missing option")
			}
			sessionID := runner.reportedSession
			if sessionID == "" {
				sessionID = "fx-session-test"
			}
			return []byte(sessionID + "\n"), nil
		}
		return []byte("idle\n"), nil
	case "display-message":
		return []byte("120\n"), nil
	default:
		return nil, nil
	}
}

func valueAfter(args []string, option string) string {
	for index := range args {
		if args[index] == option && index+1 < len(args) {
			return args[index+1]
		}
	}
	return ""
}

func TestDeliverPromptPausesBeforeSubmit(t *testing.T) {
	runner := &fakeCommandRunner{}
	runtime := &TmuxRuntime{
		Binary: "tmux-test",
		Runner: runner,
		Pause: func(_ context.Context, duration time.Duration) error {
			runner.events = append(runner.events, "pause:"+duration.String())
			return nil
		},
	}
	if err := runtime.deliverPrompt(context.Background(), "%planner", "plan-demo", "plan this"); err != nil {
		t.Fatal(err)
	}
	want := []string{"load-buffer", "paste-buffer", "pause:" + promptdelivery.SubmitDelay.String(), "send-keys"}
	if strings.Join(runner.events, ",") != strings.Join(want, ",") {
		t.Fatalf("delivery order = %v, want %v", runner.events, want)
	}
	if string(runner.prompt) != "plan this" {
		t.Fatalf("streamed prompt = %q", runner.prompt)
	}
}

func TestDeliverPromptDoesNotSubmitWhenPauseFails(t *testing.T) {
	runner := &fakeCommandRunner{}
	pauseErr := errors.New("pause failed")
	runtime := &TmuxRuntime{Binary: "tmux-test", Runner: runner, Pause: func(context.Context, time.Duration) error { return pauseErr }}
	if err := runtime.deliverPrompt(context.Background(), "%planner", "plan-demo", "plan this"); !errors.Is(err, pauseErr) {
		t.Fatalf("error = %v, want %v", err, pauseErr)
	}
	if commandSliceContains(runner.commands, "send-keys") {
		t.Fatalf("prompt submitted after pause failure: %v", runner.commands)
	}
}

func TestTmuxRuntimeCreatesThenReusesPlannerAndFocusesClient(t *testing.T) {
	runner := &fakeCommandRunner{}
	runtime := &TmuxRuntime{Binary: "tmux-test", FXCommand: "fx-tmux-test", InsideTmux: true, Runner: runner, Sleep: func(time.Duration) {}}
	plan := Plan{Project: workloadProject("demo", "/tmp/demo/project.md"), Workstream: workload.Entity{ID: "core", Project: "demo"}, RootRepository: t.TempDir(), WindowName: "planner", Prompt: "plan this"}

	first, err := runtime.Ensure(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if !first.CreatedSession || !first.CreatedPlanner {
		t.Fatalf("first launch = %#v", first)
	}
	commandCount := len(runner.commands)
	second, err := runtime.Ensure(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if second.CreatedSession || second.CreatedPlanner {
		t.Fatalf("second launch = %#v", second)
	}
	for _, forbidden := range []string{"new-session", "new-window", "split-window", "load-buffer", "paste-buffer"} {
		if commandSliceContains(runner.commands[commandCount:], forbidden) {
			t.Errorf("reuse ran %s", forbidden)
		}
	}
	if commandCountFor(runner.commands, "split-window") != 1 || runner.sidebarPaneID == "" || runner.sidebarRole != "sidebar" {
		t.Fatalf("sidebar layout was not created exactly once: commands=%#v sidebar=%q role=%q", runner.commands, runner.sidebarPaneID, runner.sidebarRole)
	}
	paneTitleSet := false
	for _, command := range runner.commands {
		if strings.Join(command, " ") == "tmux-test select-pane -t "+runner.sidebarPaneID+" -T demo" {
			paneTitleSet = true
			break
		}
	}
	if !paneTitleSet {
		t.Fatalf("sidebar pane title was not set: %#v", runner.commands)
	}
	if !commandSliceContains(runner.commands, "switch-client") || !commandSliceContains(runner.commands, "select-window") || !commandSliceContains(runner.commands, "select-pane") {
		t.Fatalf("focus commands missing: %#v", runner.commands)
	}
	last := runner.commands[len(runner.commands)-1]
	if strings.Join(last, " ") != "tmux-test select-pane -t %new" {
		t.Fatalf("planner pane was not the final focus target: %#v", last)
	}
}

func TestTmuxRuntimeTaskFirstReusesSessionAndMovesPlannerFirst(t *testing.T) {
	root := t.TempDir()
	identity, err := runtimeworkbench.NewWorkstreamIdentity("demo", "core", root)
	if err != nil {
		t.Fatal(err)
	}
	runner := &fakeCommandRunner{
		sessions: []tmuxSession{{
			ID: "$tasks", Name: identity.SessionName, Role: "workstream", Project: "demo", Workstream: "core", Repository: root,
		}},
		taskWindowID: "@task",
	}
	runtime := &TmuxRuntime{Binary: "tmux-test", FXCommand: "fx-tmux-test", Runner: runner, Sleep: func(time.Duration) {}}
	plan := Plan{Project: workloadProject("demo", "/tmp/project.md"), Workstream: workload.Entity{ID: "core", Project: "demo"}, RootRepository: root, WindowName: "plan-core", Prompt: "plan this"}
	result, err := runtime.Ensure(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if result.CreatedSession || !result.CreatedPlanner || result.SessionID != "$tasks" {
		t.Fatalf("task-first result = %#v", result)
	}
	if commandSliceContains(runner.commands, "new-session") || !commandSliceContains(runner.commands, "swap-window") {
		t.Fatalf("task-first planner did not reuse and reorder the session: %#v", runner.commands)
	}
}

func TestTmuxRuntimeMovesTaggedRepositoryPlannerIntoWorkstreamSession(t *testing.T) {
	root := t.TempDir()
	runner := &fakeCommandRunner{
		sessions:       []tmuxSession{{ID: "$root", Name: "widgets", Role: "root", Project: "example-ci", Repository: root}},
		paneSessionID:  "$root",
		paneRole:       "1",
		paneProject:    "example-ci",
		paneWorkstream: "service-cleanup",
		pane: plannerPane{
			WindowID: "@plan", WindowName: "plan-service-cleanup", PaneID: "%plan",
			CurrentPath: root, Command: "fx", State: "idle", Title: "planning", FXSessionID: "planner-live",
			Role: "planner", Project: "example-ci", Workstream: "service-cleanup",
		},
	}
	runtime := &TmuxRuntime{Binary: "tmux-test", FXCommand: "fx-tmux-test", InsideTmux: true, Runner: runner, Sleep: func(time.Duration) {}}
	plan := Plan{Project: workloadProject("example-ci", "/tmp/project.md"), Workstream: workload.Entity{ID: "service-cleanup", Project: "example-ci"}, RootRepository: root, WindowName: "plan-service-cleanup", Prompt: "do not redeliver"}
	result, err := runtime.Ensure(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if !result.CreatedSession || result.CreatedPlanner || !result.AdoptedPlanner || result.PaneID != "%plan" || result.FXSessionID != "planner-live" {
		t.Fatalf("migration result = %#v", result)
	}
	if !commandSliceContains(runner.commands, "move-window") || commandSliceContains(runner.commands, "new-window") || commandSliceContains(runner.commands, "load-buffer") {
		t.Fatalf("planner was restarted or not moved: %#v", runner.commands)
	}
	if runner.paneSessionID != result.SessionID || runner.sessions[0].Role != "root" {
		t.Fatalf("migration changed the wrong runtime: pane session=%q sessions=%#v", runner.paneSessionID, runner.sessions)
	}
}

func TestWaitSessionIDIgnoresTmuxErrorOutput(t *testing.T) {
	runner := &fakeCommandRunner{reportedSession: "fx-session-valid", sessionFailures: 1}
	runtime := &TmuxRuntime{Binary: "tmux-test", Runner: runner, Sleep: func(time.Duration) {}}

	sessionID, err := runtime.waitSessionID(context.Background(), "%planner")
	if err != nil {
		t.Fatal(err)
	}
	if sessionID != "fx-session-valid" {
		t.Fatalf("session id = %q", sessionID)
	}
}

func TestTmuxRuntimeResumesPersistedWorkstreamSession(t *testing.T) {
	root := t.TempDir()
	runner := &fakeCommandRunner{reportedSession: "resume-me"}
	runtime := &TmuxRuntime{Binary: "tmux-test", FXCommand: "fx-tmux-test", Runner: runner, Sleep: func(time.Duration) {}}
	plan := Plan{
		Project:        workloadProject("demo", "/tmp/project.md"),
		Workstream:     workload.Entity{ID: "core", Project: "demo", PlannerSessionID: "resume-me"},
		RootRepository: root, SessionName: "plan-demo", WindowName: "plan-core", Prompt: "do not redeliver",
	}
	result, err := runtime.Ensure(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if !result.CreatedPlanner || result.FXSessionID != "resume-me" {
		t.Fatalf("resume result = %#v", result)
	}
	if !commandSliceContains(runner.commands, "--resume") || !commandSliceContains(runner.commands, "resume-me") {
		t.Fatalf("resume arguments missing: %#v", runner.commands)
	}
	if commandSliceContains(runner.commands, "load-buffer") || commandSliceContains(runner.commands, "paste-buffer") {
		t.Fatalf("resumed planner received a new prompt: %#v", runner.commands)
	}
}

func TestTmuxRuntimeRefreshesProfileWhenPlannerPaneResumes(t *testing.T) {
	t.Setenv("FX_WORKBENCH_AGENT_PROFILES_ROOT", t.TempDir())
	t.Setenv("FX_WORKBENCH_PRINCIPLES_ROOT", t.TempDir())
	profile, err := profiles.Load("planner")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	runner := &fakeCommandRunner{reportedSession: "resume-me"}
	runtime := &TmuxRuntime{Binary: "tmux-test", FXCommand: "fx-tmux-test", Runner: runner, Sleep: func(time.Duration) {}}
	plan := Plan{
		Project: workloadProject("demo", "/tmp/project.md"), Workstream: workload.Entity{ID: "core", Project: "demo", PlannerSessionID: "resume-me"},
		RootRepository: root, SessionName: "plan-demo", WindowName: "plan-core", Prompt: "do not redeliver", Profile: profile,
	}
	if _, err := runtime.Ensure(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	if !commandSliceContains(runner.commands, "load-buffer") || !strings.Contains(string(runner.prompt), "WORKBENCH AGENT PROFILE planner") {
		t.Fatalf("resumed planner did not receive profile refresh: commands=%#v prompt=%q", runner.commands, runner.prompt)
	}
	if commandsContainSubstring(runner.commands, "do not redeliver") {
		t.Fatalf("resumed planner received original bootstrap: %#v", runner.commands)
	}
}

func TestTmuxRuntimeRejectsMismatchedDeterministicNameCollision(t *testing.T) {
	root := t.TempDir()
	plan := Plan{Project: workloadProject("demo", "/tmp/demo/project.md"), Workstream: workload.Entity{ID: "core", Project: "demo"}, RootRepository: root, WindowName: "planner", Prompt: "plan this"}
	identity, err := runtimeworkbench.NewWorkstreamIdentity("demo", "core", root)
	if err != nil {
		t.Fatal(err)
	}
	runner := &fakeCommandRunner{sessions: []tmuxSession{{ID: "$old", Name: identity.SessionName}}}
	runtime := &TmuxRuntime{Binary: "tmux-test", FXCommand: "fx-tmux-test", Runner: runner, Sleep: func(time.Duration) {}}
	_, err = runtime.Ensure(context.Background(), plan)
	if err == nil || !strings.Contains(err.Error(), "does not match workstream identity") {
		t.Fatalf("collision error = %v", err)
	}
}

func workloadProject(id, manifest string) workload.Entity {
	return workload.Entity{Schema: workload.SchemaV1, Type: "project", ID: id, Title: strings.ToUpper(id), SourcePath: manifest}
}

func commandsContainSubstring(commands [][]string, value string) bool {
	for _, command := range commands {
		for _, field := range command {
			if strings.Contains(field, value) {
				return true
			}
		}
	}
	return false
}

func commandSliceContains(commands [][]string, value string) bool {
	for _, command := range commands {
		for _, field := range command {
			if field == value {
				return true
			}
		}
	}
	return false
}

func commandCountFor(commands [][]string, value string) int {
	count := 0
	for _, command := range commands {
		if commandSliceContains([][]string{command}, value) {
			count++
		}
	}
	return count
}

func TestBoundedSidebarWidthLeavesPlannerPrimary(t *testing.T) {
	for width, want := range map[int]int{200: 34, 80: 24, 40: 15, 20: 9} {
		if got := boundedSidebarWidth(width); got != want {
			t.Errorf("width %d: sidebar = %d, want %d", width, got, want)
		}
		if got := boundedSidebarWidth(width); got >= width-got {
			t.Errorf("width %d: sidebar %d is not narrower than planner", width, got)
		}
	}
}
