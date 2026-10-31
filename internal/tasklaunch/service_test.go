package tasklaunch

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/jarneson/fx-workbench/internal/mainsync"
	"github.com/jarneson/fx-workbench/internal/workload"
)

type fakeAdapters struct {
	mu              sync.Mutex
	calls           []string
	fail            Stage
	validationErr   error
	createdSession  bool
	installStarted  chan struct{}
	releaseInstall  chan struct{}
	focused         chan struct{}
	installErr      error
	reportedInstall error
}

func (fake *fakeAdapters) record(stage Stage) error {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.calls = append(fake.calls, string(stage))
	if fake.fail == stage {
		return errors.New("boom")
	}
	return nil
}

func (fake *fakeAdapters) callSnapshot() []string {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return append([]string(nil), fake.calls...)
}

func (fake *fakeAdapters) Validate(context.Context, workload.Entity, string) (Target, error) {
	if err := fake.record(StageValidate); err != nil {
		return Target{}, err
	}
	return Target{
		Repository: "/repo", Worktree: "/trees/task",
		Session: "tasks-core-123", Window: "task-123", Project: "demo", Workstream: "core",
		TaskID: "task", Branch: "task", Packet: "/packet.md",
	}, nil
}
func (fake *fakeAdapters) ValidateLaunch(context.Context, Target) error { return fake.validationErr }
func (fake *fakeAdapters) Sync(context.Context, string) (mainsync.Result, error) {
	return mainsync.Result{MainWorktree: "/repo"}, fake.record(StageSyncMain)
}
func (fake *fakeAdapters) CreateWorktree(context.Context, Target) error {
	return fake.record(StageCreateWorktree)
}
func (fake *fakeAdapters) Install(ctx context.Context, _ Target) error {
	if err := fake.record(StageInstall); err != nil {
		return err
	}
	if fake.installStarted != nil {
		close(fake.installStarted)
	}
	if fake.releaseInstall != nil {
		select {
		case <-fake.releaseInstall:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return fake.installErr
}
func (fake *fakeAdapters) Launch(context.Context, Target) (Worker, error) {
	return Worker{Window: "@1", Pane: "%1", CreatedSession: fake.createdSession, CreatedWindow: true}, fake.record(StageLaunchFX)
}
func (fake *fakeAdapters) WaitReady(context.Context, string) (string, error) {
	return "fx-task-session", fake.record(StageWaitReady)
}
func (fake *fakeAdapters) TagFXSession(context.Context, Target, Worker, string) error { return nil }
func (fake *fakeAdapters) Deliver(context.Context, Target, string) error {
	return fake.record(StageDeliverTask)
}
func (fake *fakeAdapters) Focus(context.Context, Target, Worker) error {
	if err := fake.record(StageFocus); err != nil {
		return err
	}
	if fake.focused != nil {
		close(fake.focused)
	}
	return nil
}
func (fake *fakeAdapters) ReportInstall(_ context.Context, _ Worker, installErr error) error {
	fake.mu.Lock()
	fake.reportedInstall = installErr
	fake.calls = append(fake.calls, "report-install")
	fake.mu.Unlock()
	return nil
}

func TestServiceStartsInstallAndAgentConcurrentlyThenJoins(t *testing.T) {
	fake := &fakeAdapters{
		createdSession: true,
		installStarted: make(chan struct{}),
		releaseInstall: make(chan struct{}),
		focused:        make(chan struct{}),
	}
	service := &Service{Git: fake, Main: fake, Tmux: fake, Installer: fake, FX: fake}
	done := make(chan struct{})
	var result Result
	var launchErr error
	go func() {
		result, launchErr = service.Launch(context.Background(), workload.Entity{Type: "task", ID: "task"}, "task", nil)
		close(done)
	}()

	select {
	case <-fake.installStarted:
	case <-time.After(time.Second):
		t.Fatal("install did not start")
	}
	select {
	case <-fake.focused:
	case <-done:
		t.Fatal("launch completed before the blocked install")
	case <-time.After(time.Second):
		t.Fatal("agent was not focused while install was running")
	}
	select {
	case <-done:
		t.Fatal("launch completed before dependency installation")
	default:
	}
	close(fake.releaseInstall)
	<-done
	if launchErr != nil {
		t.Fatal(launchErr)
	}
	if !result.CreatedWorktree || !result.CreatedSession || !result.CreatedWindow || result.FXSessionID != "fx-task-session" {
		t.Fatalf("result = %#v", result)
	}
	calls := fake.callSnapshot()
	if index(calls, string(StageDeliverTask)) > index(calls, string(StageFocus)) || index(calls, "report-install") < index(calls, string(StageFocus)) {
		t.Fatalf("calls = %v", calls)
	}
}

func TestServiceInstallFailureIsReportedAfterAgentIsReady(t *testing.T) {
	installErr := errors.New("install broke")
	fake := &fakeAdapters{installErr: installErr}
	service := &Service{Git: fake, Main: fake, Tmux: fake, Installer: fake, FX: fake}
	result, err := service.Launch(context.Background(), workload.Entity{Type: "task"}, "task", nil)
	var failure *Failure
	if !errors.As(err, &failure) || failure.Code != FailureInstall || failure.Stage != StageInstall {
		t.Fatalf("error = %T %v", err, err)
	}
	if !errors.Is(fake.reportedInstall, installErr) {
		t.Fatalf("reported install error = %v", fake.reportedInstall)
	}
	if !result.CreatedWorktree || !result.CreatedWindow || len(failure.Retained) != 2 {
		t.Fatalf("result = %#v, failure = %#v", result, failure)
	}
}

func TestServiceReusesValidatedWorkstreamSession(t *testing.T) {
	fake := &fakeAdapters{}
	service := &Service{Git: fake, Main: fake, Tmux: fake, Installer: fake, FX: fake}
	result, err := service.Launch(context.Background(), workload.Entity{Type: "task"}, "task", nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.CreatedSession || !result.CreatedWindow {
		t.Fatalf("result = %#v", result)
	}
}

func TestServiceFailureOutcomes(t *testing.T) {
	tests := []struct {
		name     string
		stage    Stage
		code     FailureCode
		retained int
	}{
		{"sync", StageSyncMain, FailureSync, 0},
		{"readiness", StageWaitReady, FailureReadiness, 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fake := &fakeAdapters{fail: test.stage}
			service := &Service{Git: fake, Main: fake, Tmux: fake, Installer: fake, FX: fake}
			_, err := service.Launch(context.Background(), workload.Entity{Type: "task"}, "task", nil)
			var failure *Failure
			if !errors.As(err, &failure) {
				t.Fatalf("error = %T %v", err, err)
			}
			if failure.Code != test.code || failure.Stage != test.stage || len(failure.Retained) != test.retained {
				t.Fatalf("failure = %#v", failure)
			}
		})
	}
}

func TestServiceSessionIdentityConflictStopsBeforeSync(t *testing.T) {
	fake := &fakeAdapters{validationErr: errors.New("session identity mismatch")}
	service := &Service{Git: fake, Main: fake, Tmux: fake, Installer: fake, FX: fake}
	_, err := service.Launch(context.Background(), workload.Entity{Type: "task"}, "task", nil)
	var failure *Failure
	if !errors.As(err, &failure) || failure.Code != FailureConflict {
		t.Fatalf("error = %T %v", err, err)
	}
	if !reflect.DeepEqual(fake.callSnapshot(), []string{"validate"}) {
		t.Fatalf("calls = %#v", fake.callSnapshot())
	}
}

func TestServiceCancellationRetainsCreatedResources(t *testing.T) {
	fake := &fakeAdapters{}
	service := &Service{Git: fake, Main: fake, Tmux: fake, Installer: canceledInstaller{}, FX: fake}
	_, err := service.Launch(context.Background(), workload.Entity{Type: "task"}, "task", nil)
	var failure *Failure
	if !errors.As(err, &failure) || failure.Code != FailureCanceled || len(failure.Retained) != 2 {
		t.Fatalf("error = %#v", err)
	}
}

type canceledInstaller struct{}

func (canceledInstaller) Install(context.Context, Target) error { return context.Canceled }

func index(values []string, value string) int {
	for position, candidate := range values {
		if candidate == value {
			return position
		}
	}
	return -1
}
