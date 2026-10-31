package tasklaunch

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jarneson/fx-workbench/internal/mainsync"
	"github.com/jarneson/fx-workbench/internal/profiles"
	"github.com/jarneson/fx-workbench/internal/workload"
)

type Stage string

const (
	StageValidate       Stage = "validate"
	StageSyncMain       Stage = "sync-main"
	StageCreateWorktree Stage = "create-worktree"
	StageInstall        Stage = "install"
	StageLaunchFX       Stage = "launch-fx"
	StageWaitReady      Stage = "wait-ready"
	StageDeliverTask    Stage = "deliver-task"
	StageFocus          Stage = "focus"
	StageComplete       Stage = "complete"
)

type EventState string

const (
	EventStarted   EventState = "started"
	EventCompleted EventState = "completed"
	EventFailed    EventState = "failed"
)

type Progress struct {
	Stage   Stage
	State   EventState
	Message string
}

type FailureCode string

const (
	FailureConflict  FailureCode = "conflict"
	FailureSync      FailureCode = "sync-failed"
	FailureInstall   FailureCode = "install-failed"
	FailureReadiness FailureCode = "readiness-failed"
	FailureCanceled  FailureCode = "canceled"
	FailureOperation FailureCode = "operation-failed"
)

type Failure struct {
	Stage    Stage
	Code     FailureCode
	Cause    error
	Retained []string
	Recovery string
}

func (failure *Failure) Error() string {
	message := fmt.Sprintf("%s (%s): %v", failure.Stage, failure.Code, failure.Cause)
	if len(failure.Retained) > 0 {
		message += "; retained: " + strings.Join(failure.Retained, ", ")
	}
	if failure.Recovery != "" {
		message += "; recovery: " + failure.Recovery
	}
	return message
}

func (failure *Failure) Unwrap() error { return failure.Cause }

type Target struct {
	Repository string
	Worktree   string
	Session    string
	Window     string
	Project    string
	Workstream string
	TaskID     string
	Branch     string
	Title      string
	Packet     string
	Profile    profiles.Bundle
}

type Worker struct {
	Window         string
	Pane           string
	CreatedSession bool
	CreatedWindow  bool
}

type Result struct {
	Target
	FXWindow        string
	FXPane          string
	FXSessionID     string
	CreatedWorktree bool
	CreatedSession  bool
	CreatedWindow   bool
}

type Git interface {
	Validate(context.Context, workload.Entity, string) (Target, error)
	CreateWorktree(context.Context, Target) error
}

type Tmux interface {
	ValidateLaunch(context.Context, Target) error
}

type Installer interface {
	Install(context.Context, Target) error
}

type FXLauncher interface {
	Launch(context.Context, Target) (Worker, error)
	WaitReady(context.Context, string) (string, error)
	TagFXSession(context.Context, Target, Worker, string) error
	Deliver(context.Context, Target, string) error
	Focus(context.Context, Target, Worker) error
	ReportInstall(context.Context, Worker, error) error
}

type Service struct {
	Git       Git
	Main      mainsync.Synchronizer
	Tmux      Tmux
	Installer Installer
	FX        FXLauncher
}

func NewService(runtime *Runtime) *Service {
	return &Service{Git: runtime, Main: mainsync.New(), Tmux: runtime, Installer: runtime, FX: runtime}
}

type agentOutcome struct {
	result Result
	worker Worker
	stage  Stage
	err    error
}

func (service *Service) Launch(ctx context.Context, task workload.Entity, branch string, report func(Progress)) (Result, error) {
	if report == nil {
		report = func(Progress) {}
	}
	var reportMu sync.Mutex
	emit := func(progress Progress) {
		reportMu.Lock()
		defer reportMu.Unlock()
		report(progress)
	}
	result := Result{}
	run := func(stage Stage, message string, operation func() error) error {
		emit(Progress{Stage: stage, State: EventStarted, Message: message})
		if err := operation(); err != nil {
			emit(Progress{Stage: stage, State: EventFailed, Message: err.Error()})
			return service.failure(stage, err, result)
		}
		emit(Progress{Stage: stage, State: EventCompleted, Message: message})
		return nil
	}

	if err := run(StageValidate, "validating task and launch targets", func() error {
		target, err := service.Git.Validate(ctx, task, branch)
		if err != nil {
			return err
		}
		result.Target = target
		return service.Tmux.ValidateLaunch(ctx, target)
	}); err != nil {
		return result, err
	}
	if err := run(StageSyncMain, "fetching and fast-forwarding main", func() error {
		_, err := service.Main.Sync(ctx, result.Repository)
		return err
	}); err != nil {
		return result, err
	}
	if err := run(StageCreateWorktree, "creating linked worktree", func() error {
		if err := service.Git.CreateWorktree(ctx, result.Target); err != nil {
			return err
		}
		result.CreatedWorktree = true
		return nil
	}); err != nil {
		return result, err
	}

	parallelCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	installDone := make(chan error, 1)
	agentDone := make(chan agentOutcome, 1)
	go func(target Target) {
		emit(Progress{Stage: StageInstall, State: EventStarted, Message: "installing workspace dependencies"})
		err := service.Installer.Install(parallelCtx, target)
		state, message := EventCompleted, "workspace dependencies installed"
		if err != nil {
			state, message = EventFailed, err.Error()
		}
		emit(Progress{Stage: StageInstall, State: state, Message: message})
		installDone <- err
	}(result.Target)
	go func(base Result) {
		outcome := service.launchAgent(parallelCtx, base, emit)
		agentDone <- outcome
	}(result)

	var installErr error
	var agent agentOutcome
	select {
	case installErr = <-installDone:
		agent = <-agentDone
	case agent = <-agentDone:
		if agent.err != nil {
			cancel()
		}
		installErr = <-installDone
	}
	result = agent.result
	if agent.worker.Pane != "" {
		statusCtx, statusCancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		statusErr := service.FX.ReportInstall(statusCtx, agent.worker, installErr)
		statusCancel()
		if statusErr != nil {
			if agent.err != nil {
				agent.err = errors.Join(agent.err, fmt.Errorf("record install state: %w", statusErr))
			} else if installErr != nil {
				installErr = errors.Join(installErr, fmt.Errorf("record install state: %w", statusErr))
			} else {
				return result, service.failure(StageDeliverTask, fmt.Errorf("record install completion: %w", statusErr), result)
			}
		}
	}
	if agent.err != nil {
		return result, service.failure(agent.stage, agent.err, result)
	}
	if installErr != nil {
		return result, service.failure(StageInstall, installErr, result)
	}
	emit(Progress{Stage: StageComplete, State: EventCompleted, Message: "task agent ready and workspace dependencies installed"})
	return result, nil
}

func (service *Service) launchAgent(ctx context.Context, result Result, report func(Progress)) agentOutcome {
	worker := Worker{}
	run := func(stage Stage, message string, operation func() error) error {
		report(Progress{Stage: stage, State: EventStarted, Message: message})
		if err := operation(); err != nil {
			report(Progress{Stage: stage, State: EventFailed, Message: err.Error()})
			return err
		}
		report(Progress{Stage: stage, State: EventCompleted, Message: message})
		return nil
	}
	stages := []struct {
		stage     Stage
		message   string
		operation func() error
	}{
		{StageLaunchFX, "launching task worker in workstream session", func() error {
			var err error
			worker, err = service.FX.Launch(ctx, result.Target)
			result.FXWindow, result.FXPane = worker.Window, worker.Pane
			result.CreatedSession, result.CreatedWindow = worker.CreatedSession, worker.CreatedWindow
			return err
		}},
		{StageWaitReady, "waiting for fx lifecycle readiness and session identity", func() error {
			sessionID, err := service.FX.WaitReady(ctx, result.FXPane)
			result.FXSessionID = sessionID
			if err != nil {
				return err
			}
			return service.FX.TagFXSession(ctx, result.Target, worker, sessionID)
		}},
		{StageDeliverTask, "delivering task packet while dependency installation continues", func() error {
			return service.FX.Deliver(ctx, result.Target, result.FXPane)
		}},
		{StageFocus, "focusing task window while dependency installation continues", func() error {
			return service.FX.Focus(ctx, result.Target, worker)
		}},
	}
	for _, item := range stages {
		if err := run(item.stage, item.message, item.operation); err != nil {
			return agentOutcome{result: result, worker: worker, stage: item.stage, err: err}
		}
	}
	return agentOutcome{result: result, worker: worker}
}

func (service *Service) failure(stage Stage, err error, result Result) error {
	code := FailureOperation
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		code = FailureCanceled
	case stage == StageValidate:
		code = FailureConflict
	case stage == StageSyncMain:
		code = FailureSync
	case stage == StageInstall:
		code = FailureInstall
	case stage == StageWaitReady:
		code = FailureReadiness
	}
	retained := make([]string, 0, 3)
	if result.CreatedWorktree {
		retained = append(retained, "worktree "+result.Worktree)
	}
	if result.CreatedSession {
		retained = append(retained, "tmux session "+result.Session)
	}
	if result.CreatedWindow {
		window := result.FXWindow
		if window == "" {
			window = result.Window
		}
		retained = append(retained, "tmux window "+result.Session+":"+window)
	}
	recovery := "fix the reported problem and relaunch; remove retained resources manually only after checking for user changes"
	if len(retained) == 0 {
		recovery = "fix the reported preflight problem and retry"
	}
	return &Failure{Stage: stage, Code: code, Cause: err, Retained: retained, Recovery: recovery}
}

func ProgressLine(progress Progress) string {
	return fmt.Sprintf("[%s] %s: %s", progress.State, progress.Stage, progress.Message)
}

func DeadlineContext(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, timeout)
}
