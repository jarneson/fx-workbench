package tasklaunch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jarneson/fx-workbench/internal/profiles"
	"github.com/jarneson/fx-workbench/internal/promptdelivery"
	runtimeworkbench "github.com/jarneson/fx-workbench/internal/workbench"
	"github.com/jarneson/fx-workbench/internal/workload"
)

type CommandRunner interface {
	Run(context.Context, string, string, []string, io.Reader, ...string) ([]byte, error)
}

type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, directory, name string, environment []string, stdin io.Reader, args ...string) ([]byte, error) {
	command := exec.Command(name, args...)
	command.Dir = directory
	command.Env = environment
	command.Stdin = stdin
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var output strings.Builder
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Start(); err != nil {
		return nil, err
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case err := <-done:
		return []byte(output.String()), err
	case <-ctx.Done():
		_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		<-done
		return []byte(output.String()), ctx.Err()
	}
}

type Runtime struct {
	TmuxBinary     string
	WorktreeRoot   string
	InstallCommand string
	InstallShell   string
	FXCommand      string
	NoSwitch       bool
	InsideTmux     bool
	Client         string
	ReadyTimeout   time.Duration
	Runner         CommandRunner
	Sleep          func(time.Duration)
	Environment    []string
}

func NewRuntime() *Runtime {
	home, _ := os.UserHomeDir()
	tmuxBinary := os.Getenv("FX_TASK_TMUX_BIN")
	if tmuxBinary == "" {
		tmuxBinary = "tmux"
	}
	worktreeRoot := os.Getenv("FX_TASK_WORKTREE_ROOT")
	if worktreeRoot == "" {
		worktreeRoot = filepath.Join(home, "dev", "worktrees")
	}
	installCommand := os.Getenv("FX_TASK_INSTALL_COMMAND")
	installShell := os.Getenv("SHELL")
	if installShell == "" {
		installShell = "/bin/sh"
	}
	fxCommand := os.Getenv("FX_TASK_FX_COMMAND")
	if fxCommand == "" {
		fxCommand = "fx-tmux"
	}
	client := os.Getenv("FX_WORKBENCH_CLIENT")
	if strings.Contains(client, "#{") {
		client = ""
	}
	return &Runtime{
		TmuxBinary: tmuxBinary, WorktreeRoot: worktreeRoot, InstallCommand: installCommand, InstallShell: installShell,
		FXCommand: fxCommand, NoSwitch: os.Getenv("FX_TASK_NO_SWITCH") == "1", InsideTmux: os.Getenv("TMUX") != "", Client: client,
		ReadyTimeout: 15 * time.Second, Runner: ExecRunner{}, Sleep: time.Sleep, Environment: os.Environ(),
	}
}

func (runtime *Runtime) Validate(ctx context.Context, task workload.Entity, branch string) (Target, error) {
	if task.Type != "task" {
		return Target{}, fmt.Errorf("launch target is not a task: %s", task.ID)
	}
	if branch == "" {
		branch = task.Branch
	}
	if branch == "" {
		return Target{}, errors.New("branch is required")
	}
	packetInfo, err := os.Stat(task.SourcePath)
	if err != nil || !packetInfo.Mode().IsRegular() {
		return Target{}, fmt.Errorf("task file is not readable: %s", task.SourcePath)
	}
	if packetInfo.Size() > 48*1024 {
		return Target{}, fmt.Errorf("task file exceeds 48 KiB: %s", task.SourcePath)
	}
	if output, err := runtime.run(ctx, "", "git", "check-ref-format", "--branch", branch); err != nil {
		return Target{}, fmt.Errorf("invalid branch %q: %s", branch, commandError(output, err))
	}
	if !filepath.IsAbs(task.Repository) {
		return Target{}, fmt.Errorf("task repository must be absolute: %s", task.Repository)
	}
	rootOutput, err := runtime.run(ctx, task.Repository, "git", "rev-parse", "--show-toplevel")
	if err != nil {
		return Target{}, fmt.Errorf("resolve repository root: %s", commandError(rootOutput, err))
	}
	repository := strings.TrimSpace(string(rootOutput))
	remoteOutput, err := runtime.run(ctx, repository, "git", "remote", "get-url", "origin")
	if err != nil {
		return Target{}, errors.New("repository has no origin remote")
	}
	repositoryName := strings.TrimSuffix(filepath.Base(strings.TrimSpace(string(remoteOutput))), ".git")
	worktreeName := strings.ReplaceAll(branch, "/", "-")
	worktree := filepath.Join(runtime.WorktreeRoot, repositoryName, worktreeName)
	if _, err := os.Lstat(worktree); err == nil {
		return Target{}, fmt.Errorf("worktree path already exists: %s", worktree)
	} else if !errors.Is(err, os.ErrNotExist) {
		return Target{}, fmt.Errorf("inspect worktree path: %w", err)
	}
	if task.Project == "" || task.Workstream == "" || task.ID == "" {
		return Target{}, errors.New("canonical task requires project, workstream, and task IDs")
	}
	identity, err := runtimeworkbench.NewWorkstreamIdentity(task.Project, task.Workstream, repository)
	if err != nil {
		return Target{}, fmt.Errorf("build workstream session identity: %w", err)
	}
	session := identity.SessionName
	window := windowName(task.ID)
	title := task.Title
	if title == "" {
		title = branch
	}
	profile, err := profiles.Load("coding")
	if err != nil {
		return Target{}, fmt.Errorf("load coding agent profile: %w", err)
	}
	return Target{
		Repository: repository, Worktree: worktree,
		Session: session, Window: window, Project: task.Project, Workstream: task.Workstream,
		TaskID: task.ID, Branch: branch, Title: title, Packet: task.SourcePath, Profile: profile,
	}, nil
}

func (runtime *Runtime) CreateWorktree(ctx context.Context, target Target) error {
	if err := os.MkdirAll(filepath.Dir(target.Worktree), 0o755); err != nil {
		return err
	}
	args := []string{"worktree", "add"}
	if _, err := runtime.run(ctx, target.Repository, "git", "show-ref", "--verify", "--quiet", "refs/heads/"+target.Branch); err == nil {
		args = append(args, target.Worktree, target.Branch)
	} else if _, err := runtime.run(ctx, target.Repository, "git", "ls-remote", "--exit-code", "--heads", "origin", "refs/heads/"+target.Branch); err == nil {
		refspec := "refs/heads/" + target.Branch + ":refs/remotes/origin/" + target.Branch
		if output, fetchErr := runtime.run(ctx, target.Repository, "git", "fetch", "origin", refspec); fetchErr != nil {
			return fmt.Errorf("fetch remote branch %s: %s", target.Branch, commandError(output, fetchErr))
		}
		args = append(args, "--track", "-b", target.Branch, target.Worktree, "origin/"+target.Branch)
	} else {
		args = append(args, "-b", target.Branch, target.Worktree, "main")
	}
	if output, err := runtime.run(ctx, target.Repository, "git", args...); err != nil {
		return fmt.Errorf("git worktree add: %s", commandError(output, err))
	}
	return nil
}

func (runtime *Runtime) sessionExists(ctx context.Context, session string) (bool, error) {
	output, err := runtime.tmux(ctx, "has-session", "-t="+session)
	if err == nil {
		return true, nil
	}
	message := strings.ToLower(string(output))
	if strings.Contains(message, "can't find session") || strings.Contains(message, "no server running") || strings.Contains(message, "failed to connect") || strings.Contains(message, "error connecting") {
		return false, nil
	}
	return false, fmt.Errorf("tmux has-session: %s", commandError(output, err))
}

func (runtime *Runtime) ValidateLaunch(ctx context.Context, target Target) error {
	exists, err := runtime.sessionExists(ctx, target.Session)
	if err != nil || !exists {
		return err
	}
	identity, identityErr := runtimeworkbench.NewWorkstreamIdentity(target.Project, target.Workstream, target.Repository)
	if identityErr != nil {
		return identityErr
	}
	expected := identity.SessionOptions()
	for _, option := range expected {
		output, showErr := runtime.tmux(ctx, "show-options", "-v", "-t", target.Session, option[0])
		if showErr != nil || strings.TrimSpace(string(output)) != option[1] {
			return fmt.Errorf("tmux session %s does not match %s %q", target.Session, option[0], option[1])
		}
	}
	format := "#{window_id}|#{@fx_task_id}"
	output, err := runtime.tmux(ctx, "list-windows", "-t", target.Session, "-F", format)
	if err != nil {
		return fmt.Errorf("tmux list-windows: %s", commandError(output, err))
	}
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		fields := strings.SplitN(line, "|", 2)
		if len(fields) == 2 && fields[1] == target.TaskID {
			return fmt.Errorf("task %s already has tmux window %s in session %s", target.TaskID, fields[0], target.Session)
		}
	}
	return nil
}

func (runtime *Runtime) tagSession(ctx context.Context, target Target) error {
	identity, err := runtimeworkbench.NewWorkstreamIdentity(target.Project, target.Workstream, target.Repository)
	if err != nil {
		return err
	}
	for _, option := range identity.SessionOptions() {
		if output, err := runtime.tmux(ctx, "set-option", "-t", target.Session, option[0], option[1]); err != nil {
			return fmt.Errorf("tmux set-option %s: %s", option[0], commandError(output, err))
		}
	}
	if output, err := runtime.tmux(ctx, "set-option", "-t", target.Session, "renumber-windows", "on"); err != nil {
		return fmt.Errorf("tmux set-option renumber-windows: %s", commandError(output, err))
	}
	return nil
}

func (runtime *Runtime) tagWorker(ctx context.Context, target Target, worker Worker) error {
	options := [][2]string{
		{"@fx_task_role", "task"},
		{"@fx_task_id", target.TaskID},
		{"@fx_repository", target.Repository},
		{"@fx_worktree", target.Worktree},
		{"@fx_branch", target.Branch},
		{"@fx_agent_profile", target.Profile.ID},
		{"@fx_agent_profile_digest", target.Profile.Digest},
		{"@fx_install_state", "running"},
	}
	for _, option := range options {
		if output, err := runtime.tmux(ctx, "set-option", "-w", "-t", worker.Window, option[0], option[1]); err != nil {
			return fmt.Errorf("tmux set-window-option %s: %s", option[0], commandError(output, err))
		}
		if output, err := runtime.tmux(ctx, "set-option", "-p", "-t", worker.Pane, option[0], option[1]); err != nil {
			return fmt.Errorf("tmux set-pane-option %s: %s", option[0], commandError(output, err))
		}
	}
	return nil
}

type packageManifest struct {
	PackageManager string `json:"packageManager"`
}

type installInvocation struct {
	name  string
	args  []string
	label string
}

func (runtime *Runtime) installInvocation(worktree string) (installInvocation, error) {
	if runtime.InstallCommand != "" {
		return installInvocation{name: runtime.InstallShell, args: []string{"-lc", runtime.InstallCommand}, label: runtime.InstallCommand}, nil
	}
	manifestBytes, err := os.ReadFile(filepath.Join(worktree, "package.json"))
	if errors.Is(err, os.ErrNotExist) {
		return installInvocation{}, nil
	}
	if err != nil {
		return installInvocation{}, fmt.Errorf("read package.json: %w", err)
	}
	var manifest packageManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return installInvocation{}, fmt.Errorf("parse package.json: %w", err)
	}
	if manifest.PackageManager == "" {
		return installInvocation{name: "npm", args: []string{"install"}, label: "npm install"}, nil
	}
	separator := strings.LastIndex(manifest.PackageManager, "@")
	if separator <= 0 || separator == len(manifest.PackageManager)-1 {
		return installInvocation{}, fmt.Errorf("invalid packageManager %q", manifest.PackageManager)
	}
	manager := manifest.PackageManager[:separator]
	version := manifest.PackageManager[separator+1:]
	switch manager {
	case "pnpm":
		majorText, _, _ := strings.Cut(version, ".")
		major, err := strconv.Atoi(majorText)
		if err != nil {
			return installInvocation{}, fmt.Errorf("parse pnpm version in packageManager %q", manifest.PackageManager)
		}
		args := []string{"pnpm", "install"}
		if major >= 12 {
			args = []string{"pnpm", "--config.virtual-store-type=global", "install"}
		}
		return installInvocation{name: "corepack", args: args, label: "corepack " + strings.Join(args, " ")}, nil
	case "yarn":
		return installInvocation{name: "corepack", args: []string{"yarn", "install"}, label: "corepack yarn install"}, nil
	case "npm", "bun":
		return installInvocation{name: manager, args: []string{"install"}, label: manager + " install"}, nil
	default:
		return installInvocation{}, fmt.Errorf("unsupported packageManager %q", manifest.PackageManager)
	}
}

func (runtime *Runtime) Install(ctx context.Context, target Target) error {
	invocation, err := runtime.installInvocation(target.Worktree)
	if err != nil {
		return err
	}
	if invocation.name == "" {
		return nil
	}
	output, err := runtime.run(ctx, target.Worktree, invocation.name, invocation.args...)
	if err != nil {
		return fmt.Errorf("%s: %s", invocation.label, commandError(output, err))
	}
	return nil
}

func (runtime *Runtime) Launch(ctx context.Context, target Target) (Worker, error) {
	exists, err := runtime.sessionExists(ctx, target.Session)
	if err != nil {
		return Worker{}, err
	}
	worker := Worker{CreatedWindow: true}
	var output []byte
	if exists {
		if err := runtime.ValidateLaunch(ctx, target); err != nil {
			return Worker{}, err
		}
		output, err = runtime.tmux(ctx, "new-window", "-d", "-P", "-F", "#{window_id}|#{pane_id}", "-t", target.Session+":", "-n", target.Window, "-c", target.Worktree, runtime.FXCommand)
	} else {
		output, err = runtime.tmux(ctx, "new-session", "-d", "-P", "-F", "#{window_id}|#{pane_id}", "-s", target.Session, "-n", target.Window, "-c", target.Worktree, runtime.FXCommand)
		worker.CreatedSession = err == nil
	}
	if err != nil {
		worker.CreatedWindow = false
		return worker, fmt.Errorf("tmux launch task window %s:%s: %s", target.Session, target.Window, commandError(output, err))
	}
	fields := strings.Split(strings.TrimSpace(string(output)), "|")
	if len(fields) != 2 || fields[0] == "" || fields[1] == "" {
		return worker, fmt.Errorf("tmux task launch returned malformed identity: %q", strings.TrimSpace(string(output)))
	}
	worker.Window, worker.Pane = fields[0], fields[1]
	if worker.CreatedSession {
		if err := runtime.tagSession(ctx, target); err != nil {
			return worker, err
		}
	}
	if err := runtime.tagWorker(ctx, target, worker); err != nil {
		return worker, err
	}
	return worker, nil
}

func (runtime *Runtime) TagFXSession(ctx context.Context, _ Target, worker Worker, sessionID string) error {
	for _, args := range [][]string{
		{"set-option", "-w", "-t", worker.Window, "@fx_session_id", sessionID},
		{"set-option", "-p", "-t", worker.Pane, "@fx_session_id", sessionID},
	} {
		if output, err := runtime.tmux(ctx, args...); err != nil {
			return fmt.Errorf("tmux tag fx session: %s", commandError(output, err))
		}
	}
	return nil
}

func (runtime *Runtime) WaitReady(ctx context.Context, pane string) (string, error) {
	readyCtx, cancel := context.WithTimeout(ctx, runtime.ReadyTimeout)
	defer cancel()
	for {
		stateOutput, _ := runtime.tmux(readyCtx, "show-options", "-p", "-v", "-t", pane, "@fx_state")
		sessionOutput, _ := runtime.tmux(readyCtx, "show-options", "-p", "-v", "-t", pane, "@fx_session_id")
		sessionID := strings.TrimSpace(string(sessionOutput))
		if strings.TrimSpace(string(stateOutput)) == "idle" && sessionID != "" {
			return sessionID, nil
		}
		select {
		case <-readyCtx.Done():
			if errors.Is(readyCtx.Err(), context.DeadlineExceeded) {
				return "", fmt.Errorf("fx did not report readiness and session identity in pane %s before %s timeout", pane, runtime.ReadyTimeout)
			}
			return "", readyCtx.Err()
		default:
			runtime.Sleep(50 * time.Millisecond)
		}
	}
}

type DependencyGuidance int

const (
	DependencyAbsent DependencyGuidance = iota
	DependencyRunning
	DependencyComplete
	DependencyFailed
)

func WorkerPrompt(target Target, absolutePacket string, packet []byte) string {
	return fmt.Sprintf("%s\nStart work on the task below in this worktree.\n\nTask packet: %s\n\n%s\n\n%s", target.Profile.Text, absolutePacket, packet, WorkerGuidance(target.Project, target.TaskID, absolutePacket, DependencyRunning))
}

func WorkerGuidance(project, taskID, packet string, dependency DependencyGuidance) string {
	var dependencyText string
	switch dependency {
	case DependencyAbsent:
		dependencyText = "Workbench did not start dependency installation for this resume, adoption, or guidance refresh. An absent `@fx_install_state` marker is not a blocker; validate the existing workspace dependencies normally before running dependency-sensitive commands."
	case DependencyRunning:
		dependencyText = "Dependency installation is running concurrently. You can inspect and edit source now, but wait for `tmux show-options -p -v @fx_install_state` to report `complete` before running dependency-sensitive commands. A `failed` result means you must fix the install first."
	case DependencyComplete:
		dependencyText = "Dependency installation for this workspace is complete; dependency-sensitive commands can run."
	case DependencyFailed:
		dependencyText = "The existing dependency installation for this workspace failed. Fix that install failure before running dependency-sensitive commands."
	default:
		panic(fmt.Sprintf("unknown dependency guidance %d", dependency))
	}
	return fmt.Sprintf("Current FX Workbench task-worker guidance:\n- Work only in the current task worktree; do not create another branch or worktree.\n- %s\n- Re-read the task packet at %s and applicable AGENTS.md files before making decisions.\n- Implement the task and run focused local validation.\n- When authoring is complete, append concise outcomes to the packet and run `fx-workbench status --project %s --set self-review %s`; otherwise set it blocked and record the blocker.\n- Do not commit, push, or open a pull request unless explicitly requested.\n- If explicitly requested to create a pull request, create it ready for review rather than as a draft. After PR creation succeeds, immediately run `fx-workbench status --project %s --set in-review %s`, ask the user which reviewer to request, and add that reviewer without guessing.", dependencyText, packet, project, taskID, project, taskID)
}

func (runtime *Runtime) Deliver(ctx context.Context, target Target, pane string) error {
	// Lifecycle readiness can precede tmux processing fx's terminal-mode requests.
	if err := runtime.pause(ctx, 200*time.Millisecond); err != nil {
		return err
	}
	title := []rune(strings.TrimSpace(target.Title))
	if len(title) > 60 {
		title = title[:60]
	}
	if len(title) > 0 {
		if err := runtime.sendLiteral(ctx, pane, "/rename "+string(title)); err != nil {
			return err
		}
		if _, err := runtime.tmux(ctx, "send-keys", "-t", pane, "Enter"); err != nil {
			return err
		}
		if err := runtime.pause(ctx, 100*time.Millisecond); err != nil {
			return err
		}
	}
	packet, err := os.ReadFile(target.Packet)
	if err != nil {
		return fmt.Errorf("read task packet: %w", err)
	}
	absolutePacket, err := filepath.Abs(target.Packet)
	if err != nil {
		return err
	}
	prompt := WorkerPrompt(target, absolutePacket, packet)
	buffer := "fx-task-" + safeComponent(target.Session) + "-" + strconv.Itoa(os.Getpid())
	if err := promptdelivery.Paste(ctx, func(ctx context.Context, stdin io.Reader, args ...string) ([]byte, error) {
		return runtime.tmuxInput(ctx, stdin, args...)
	}, buffer, pane, []byte(prompt)); err != nil {
		return err
	}
	if err := runtime.pause(ctx, promptdelivery.SubmitDelay); err != nil {
		return err
	}
	if output, err := runtime.tmux(ctx, "send-keys", "-t", pane, "Enter"); err != nil {
		return fmt.Errorf("tmux submit prompt: %s", commandError(output, err))
	}
	return nil
}

func (runtime *Runtime) ReportInstall(ctx context.Context, worker Worker, installErr error) error {
	state := "complete"
	if installErr != nil {
		state = "failed"
	}
	for _, args := range [][]string{
		{"set-option", "-w", "-t", worker.Window, "@fx_install_state", state},
		{"set-option", "-p", "-t", worker.Pane, "@fx_install_state", state},
	} {
		if output, err := runtime.tmux(ctx, args...); err != nil {
			return fmt.Errorf("tmux record install state: %s", commandError(output, err))
		}
	}
	return nil
}

func (runtime *Runtime) Focus(ctx context.Context, target Target, worker Worker) error {
	if runtime.NoSwitch {
		return nil
	}
	if runtime.InsideTmux || runtime.Client != "" {
		args := []string{"switch-client"}
		if runtime.Client != "" {
			args = append(args, "-c", runtime.Client)
		}
		args = append(args, "-t", target.Session)
		if output, err := runtime.tmux(ctx, args...); err != nil {
			return fmt.Errorf("tmux switch-client: %s", commandError(output, err))
		}
	}
	if output, err := runtime.tmux(ctx, "select-window", "-t", worker.Window); err != nil {
		return fmt.Errorf("tmux select-window: %s", commandError(output, err))
	}
	if output, err := runtime.tmux(ctx, "select-pane", "-t", worker.Pane); err != nil {
		return fmt.Errorf("tmux select-pane: %s", commandError(output, err))
	}
	return nil
}

func (runtime *Runtime) sendLiteral(ctx context.Context, pane, value string) error {
	output, err := runtime.tmux(ctx, "send-keys", "-t", pane, "-l", value)
	if err != nil {
		return fmt.Errorf("tmux send-keys: %s", commandError(output, err))
	}
	return nil
}

func (runtime *Runtime) pause(ctx context.Context, duration time.Duration) error {
	done := make(chan struct{})
	go func() {
		runtime.Sleep(duration)
		close(done)
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return nil
	}
}

func (runtime *Runtime) tmux(ctx context.Context, args ...string) ([]byte, error) {
	return runtime.tmuxInput(ctx, nil, args...)
}

func (runtime *Runtime) tmuxInput(ctx context.Context, stdin io.Reader, args ...string) ([]byte, error) {
	return runtime.runInput(ctx, "", runtime.TmuxBinary, stdin, args...)
}

func (runtime *Runtime) run(ctx context.Context, directory, name string, args ...string) ([]byte, error) {
	return runtime.runInput(ctx, directory, name, nil, args...)
}

func (runtime *Runtime) runInput(ctx context.Context, directory, name string, stdin io.Reader, args ...string) ([]byte, error) {
	return runtime.Runner.Run(ctx, directory, name, runtime.Environment, stdin, args...)
}

func windowName(taskID string) string {
	return fmt.Sprintf("%s-%s", safeComponent(taskID), valueHash(taskID)[:6])
}

func valueHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:10]
}

func safeComponent(value string) string {
	var builder strings.Builder
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '_' || character == '-' {
			builder.WriteRune(character)
		} else if builder.Len() > 0 {
			builder.WriteByte('-')
		}
	}
	result := strings.Trim(builder.String(), "-_")
	if result == "" {
		result = "workspace"
	}
	if len(result) > 32 {
		result = result[:32]
	}
	return result
}

func commandError(output []byte, err error) string {
	if message := strings.TrimSpace(string(output)); message != "" {
		return message
	}
	return err.Error()
}
