package tasklaunch

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jarneson/fx-workbench/internal/profiles"
	"github.com/jarneson/fx-workbench/internal/promptdelivery"
	runtimeworkbench "github.com/jarneson/fx-workbench/internal/workbench"
)

type deliveryRunner struct {
	events  []string
	payload []byte
}

type recordedCommand struct {
	directory string
	name      string
	args      []string
}

type installRunner struct {
	commands []recordedCommand
}

func (runner *installRunner) Run(_ context.Context, directory, name string, _ []string, _ io.Reader, args ...string) ([]byte, error) {
	runner.commands = append(runner.commands, recordedCommand{directory: directory, name: name, args: append([]string(nil), args...)})
	return nil, nil
}

func (runner *deliveryRunner) Run(_ context.Context, _, name string, _ []string, stdin io.Reader, args ...string) ([]byte, error) {
	runner.events = append(runner.events, name+":"+args[0])
	if stdin != nil {
		var err error
		runner.payload, err = io.ReadAll(stdin)
		if err != nil {
			return nil, err
		}
	}
	return nil, nil
}

func TestInstallUsesPinnedPnpm12GlobalVirtualStore(t *testing.T) {
	worktree := t.TempDir()
	if err := os.WriteFile(filepath.Join(worktree, "package.json"), []byte(`{"packageManager":"pnpm@12.14.0"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := &installRunner{}
	runtime := &Runtime{Runner: runner, Environment: os.Environ(), InstallShell: "/bin/sh"}
	if err := runtime.Install(context.Background(), Target{Worktree: worktree}); err != nil {
		t.Fatal(err)
	}
	if len(runner.commands) != 1 {
		t.Fatalf("commands = %#v", runner.commands)
	}
	if got := runner.commands[0]; got.name != "corepack" || strings.Join(got.args, " ") != "pnpm --config.virtual-store-type=global install" || got.directory != worktree {
		t.Fatalf("install command = %#v", got)
	}
}

func TestInstallFallsBackForOlderPnpmAndOtherPackageManagers(t *testing.T) {
	tests := []struct {
		name            string
		packageManager  string
		wantCommandName string
		wantArgs        string
	}{
		{name: "older pnpm", packageManager: "pnpm@11.9.0", wantCommandName: "corepack", wantArgs: "pnpm install"},
		{name: "yarn", packageManager: "yarn@4.10.3", wantCommandName: "corepack", wantArgs: "yarn install"},
		{name: "npm", packageManager: "npm@11.6.1", wantCommandName: "npm", wantArgs: "install"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			worktree := t.TempDir()
			manifest := `{"packageManager":"` + test.packageManager + `"}`
			if err := os.WriteFile(filepath.Join(worktree, "package.json"), []byte(manifest), 0o644); err != nil {
				t.Fatal(err)
			}
			runner := &installRunner{}
			runtime := &Runtime{Runner: runner, Environment: os.Environ(), InstallShell: "/bin/sh"}
			if err := runtime.Install(context.Background(), Target{Worktree: worktree}); err != nil {
				t.Fatal(err)
			}
			got := runner.commands[len(runner.commands)-1]
			if got.name != test.wantCommandName || strings.Join(got.args, " ") != test.wantArgs {
				t.Fatalf("install command = %#v", got)
			}
		})
	}
}

func TestInstallSkipsRepositoryWithoutPackageManifest(t *testing.T) {
	runner := &installRunner{}
	runtime := &Runtime{Runner: runner, Environment: os.Environ(), InstallShell: "/bin/sh"}
	if err := runtime.Install(context.Background(), Target{Worktree: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if len(runner.commands) != 0 {
		t.Fatalf("commands = %#v", runner.commands)
	}
}

func TestInstallCommandOverrideRemainsAuthoritative(t *testing.T) {
	runner := &installRunner{}
	runtime := &Runtime{Runner: runner, Environment: os.Environ(), InstallShell: "/bin/zsh", InstallCommand: "custom install"}
	if err := runtime.Install(context.Background(), Target{Worktree: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if len(runner.commands) != 1 || runner.commands[0].name != "/bin/zsh" || strings.Join(runner.commands[0].args, " ") != "-lc custom install" {
		t.Fatalf("commands = %#v", runner.commands)
	}
}

func TestReportInstallUpdatesWorkerStateWithoutSteeringAgent(t *testing.T) {
	for _, test := range []struct {
		name       string
		installErr error
		wantState  string
	}{
		{name: "complete", wantState: "complete"},
		{name: "failed", installErr: errors.New("boom"), wantState: "failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &installRunner{}
			runtime := &Runtime{TmuxBinary: "tmux-test", Runner: runner, Environment: os.Environ()}
			if err := runtime.ReportInstall(context.Background(), Worker{Window: "@1", Pane: "%1"}, test.installErr); err != nil {
				t.Fatal(err)
			}
			if len(runner.commands) != 2 {
				t.Fatalf("commands = %#v", runner.commands)
			}
			for _, command := range runner.commands {
				if command.name != "tmux-test" || command.args[len(command.args)-2] != "@fx_install_state" || command.args[len(command.args)-1] != test.wantState {
					t.Fatalf("state command = %#v", command)
				}
			}
		})
	}
}

func TestDeliverPausesBetweenPasteAndSubmit(t *testing.T) {
	packet := filepath.Join(t.TempDir(), "task.md")
	if err := os.WriteFile(packet, []byte("# Task\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := &deliveryRunner{}
	runtime := &Runtime{
		TmuxBinary: "tmux-test",
		Runner:     runner,
		Sleep: func(duration time.Duration) {
			runner.events = append(runner.events, "pause:"+duration.String())
		},
	}
	if err := runtime.Deliver(context.Background(), Target{Title: "Task", Packet: packet, Session: "demo"}, "%worker"); err != nil {
		t.Fatal(err)
	}
	load := -1
	for index, event := range runner.events {
		if event == "tmux-test:load-buffer" {
			load = index
			break
		}
	}
	want := []string{"tmux-test:load-buffer", "tmux-test:paste-buffer", "pause:" + promptdelivery.SubmitDelay.String(), "tmux-test:send-keys"}
	if load < 0 || load+len(want) > len(runner.events) || strings.Join(runner.events[load:load+len(want)], ",") != strings.Join(want, ",") {
		t.Fatalf("delivery events = %v, want consecutive %v", runner.events, want)
	}
	if !strings.Contains(string(runner.payload), "# Task\n") {
		t.Fatalf("streamed prompt = %q", runner.payload)
	}
}

func TestExecRunnerCancellationStopsProcessGroup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires unix process groups")
	}
	marker := filepath.Join(t.TempDir(), "survived")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := (ExecRunner{}).Run(ctx, "", "/bin/sh", os.Environ(), nil, "-c", "sleep 1; echo survived > \""+marker+"\"")
		done <- err
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context canceled", err)
	}
	time.Sleep(1100 * time.Millisecond)
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled child process wrote marker: %v", err)
	}
}

func TestTaskLaunchUsesSharedWorkstreamSessionIdentity(t *testing.T) {
	first, err := runtimeworkbench.NewWorkstreamIdentity("demo", "core", "/repos/widgets")
	if err != nil {
		t.Fatal(err)
	}
	again, err := runtimeworkbench.NewWorkstreamIdentity("demo", "core", "/repos/widgets")
	if err != nil || first.SessionName != again.SessionName {
		t.Fatal("session identity is not stable")
	}
	for _, input := range [][3]string{{"other", "core", "/repos/widgets"}, {"demo", "other", "/repos/widgets"}, {"demo", "core", "/other/widgets"}} {
		other, identityErr := runtimeworkbench.NewWorkstreamIdentity(input[0], input[1], input[2])
		if identityErr != nil {
			t.Fatal(identityErr)
		}
		if first.SessionName == other.SessionName {
			t.Fatalf("session identity collision: %q", first.SessionName)
		}
	}
	if windowName("task/one") != windowName("task/one") || windowName("task/one") == windowName("task-one") {
		t.Fatal("task window identity is not deterministic and collision-safe")
	}
}

func TestWorkerPromptIncludesCodingProfileBeforeTask(t *testing.T) {
	t.Setenv("FX_WORKBENCH_AGENT_PROFILES_ROOT", t.TempDir())
	t.Setenv("FX_WORKBENCH_PRINCIPLES_ROOT", t.TempDir())
	profile, err := profiles.Load("coding")
	if err != nil {
		t.Fatal(err)
	}
	prompt := WorkerPrompt(Target{Project: "demo", TaskID: "ship", Profile: profile}, "/packet.md", []byte("# Ship\n"))
	profileIndex := strings.Index(prompt, "WORKBENCH AGENT PROFILE coding")
	taskIndex := strings.Index(prompt, "Task packet: /packet.md")
	if profileIndex < 0 || taskIndex < 0 || profileIndex > taskIndex {
		t.Fatalf("profile/task order is wrong:\n%s", prompt)
	}
	for _, expected := range []string{"Do Less", "Prove It Works", "Human Fucking Language", "Dependency installation is running concurrently"} {
		if !strings.Contains(prompt, expected) {
			t.Errorf("prompt missing %q", expected)
		}
	}
}

func TestWorkerGuidanceDescribesDependencyState(t *testing.T) {
	tests := []struct {
		name       string
		dependency DependencyGuidance
		want       string
		forbidden  string
	}{
		{name: "running", dependency: DependencyRunning, want: "Dependency installation is running concurrently", forbidden: "did not start dependency installation"},
		{name: "complete", dependency: DependencyComplete, want: "Dependency installation for this workspace is complete", forbidden: "wait for `tmux show-options"},
		{name: "failed", dependency: DependencyFailed, want: "existing dependency installation for this workspace failed", forbidden: "is running concurrently"},
		{name: "absent", dependency: DependencyAbsent, want: "An absent `@fx_install_state` marker is not a blocker", forbidden: "is running concurrently"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			guidance := WorkerGuidance("demo", "ship-it", "/vault/ship-it.md", test.dependency)
			if !strings.Contains(guidance, test.want) {
				t.Fatalf("guidance missing %q:\n%s", test.want, guidance)
			}
			if strings.Contains(guidance, test.forbidden) {
				t.Fatalf("guidance contains %q:\n%s", test.forbidden, guidance)
			}
		})
	}
}

func TestWorkerGuidanceIncludesCurrentPullRequestLifecycle(t *testing.T) {
	guidance := WorkerGuidance("demo", "ship-it", "/vault/ship-it.md", DependencyRunning)
	for _, expected := range []string{
		"Re-read the task packet at /vault/ship-it.md",
		"--set self-review ship-it",
		"create it ready for review rather than as a draft",
		"--set in-review ship-it",
	} {
		if !strings.Contains(guidance, expected) {
			t.Errorf("guidance missing %q:\n%s", expected, guidance)
		}
	}
}
