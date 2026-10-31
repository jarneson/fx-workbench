package planner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/jarneson/fx-workbench/internal/mainsync"
	"github.com/jarneson/fx-workbench/internal/profiles"
	runtimeworkbench "github.com/jarneson/fx-workbench/internal/workbench"
	"github.com/jarneson/fx-workbench/internal/workload"
)

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

type Catalog interface {
	ResolveProject(cwd, explicit string) (workload.ProjectResolution, error)
	List(workload.ListOptions) ([]workload.Entity, error)
	SetWorkstreamPlannerSession(workload.Entity, string) error
}

type Runtime interface {
	Ensure(context.Context, Plan) (LaunchResult, error)
}

type Plan struct {
	Project        workload.Entity
	Workstream     workload.Entity
	RootRepository string
	WorkspaceID    string
	SessionName    string
	WindowName     string
	Repositories   []string
	Workstreams    []workload.Entity
	Tasks          []workload.Entity
	Profile        profiles.Bundle
	Prompt         string
}

type LaunchResult struct {
	MainWorktree   string
	SessionName    string
	SessionID      string
	WindowName     string
	WindowID       string
	PaneID         string
	FXSessionID    string
	CreatedSession bool
	AdoptedSession bool
	CreatedPlanner bool
	AdoptedPlanner bool
}

func Build(catalog Catalog, cwd, explicitProject, explicitWorkstream string) (Plan, error) {
	resolution, err := catalog.ResolveProject(cwd, explicitProject)
	if err != nil {
		return Plan{}, err
	}
	project := resolution.Project
	if project.Schema != workload.SchemaV1 || project.Legacy || project.RootRepository == "" {
		return Plan{}, fmt.Errorf("project %s has no canonical root_repository; migrate it with `fx-workbench project onboard --id <new-project-id> --title %q --root-repository <absolute-repository>`", project.ID, project.Title)
	}
	entities, err := catalog.List(workload.ListOptions{Project: project.ID})
	if err != nil {
		return Plan{}, err
	}
	var workstreams []workload.Entity
	for _, entity := range entities {
		if entity.Type == "workstream" && entity.Schema == workload.SchemaV1 && !entity.Legacy && entity.Status != "closed" {
			workstreams = append(workstreams, entity)
		}
	}
	sortEntities(workstreams)
	var workstream workload.Entity
	if explicitWorkstream != "" {
		for _, candidate := range workstreams {
			if candidate.ID == explicitWorkstream {
				workstream = candidate
				break
			}
		}
		if workstream.ID == "" {
			return Plan{}, fmt.Errorf("active workstream not found in project %s: %s", project.ID, explicitWorkstream)
		}
	} else if len(workstreams) == 1 {
		workstream = workstreams[0]
	} else {
		return Plan{}, fmt.Errorf("project %s has %d active workstreams; choose one with --workstream", project.ID, len(workstreams))
	}
	rootRepository := workstream.Repository
	if rootRepository == "" {
		rootRepository = project.RootRepository
	}
	if !filepath.IsAbs(rootRepository) {
		return Plan{}, fmt.Errorf("workstream %s repository must be absolute: %q", workstream.ID, rootRepository)
	}
	info, err := os.Stat(rootRepository)
	if err != nil || !info.IsDir() {
		return Plan{}, fmt.Errorf("workstream %s repository does not exist: %s", workstream.ID, rootRepository)
	}
	root, err := filepath.EvalSymlinks(rootRepository)
	if err != nil {
		return Plan{}, fmt.Errorf("resolve workstream repository: %w", err)
	}
	identity, err := runtimeworkbench.NewWorkstreamIdentity(project.ID, workstream.ID, root)
	if err != nil {
		return Plan{}, fmt.Errorf("build workstream session identity: %w", err)
	}
	var tasks []workload.Entity
	for _, entity := range entities {
		if entity.Type == "task" && entity.Workstream == workstream.ID && entity.Schema == workload.SchemaV1 && !entity.Legacy && entity.Status != "merged" && entity.Status != "superseded" {
			tasks = append(tasks, entity)
		}
	}
	sortEntities(tasks)
	profile, err := profiles.Load("planner")
	if err != nil {
		return Plan{}, fmt.Errorf("load planner agent profile: %w", err)
	}
	plan := Plan{
		Project: project, Workstream: workstream, RootRepository: identity.Repository, WorkspaceID: identity.WorkspaceID,
		SessionName: identity.SessionName, WindowName: "plan-" + safeComponent(workstream.ID),
		Repositories: append([]string(nil), project.Repositories...), Workstreams: []workload.Entity{workstream}, Tasks: tasks, Profile: profile,
	}
	plan.Prompt = buildPrompt(plan)
	return plan, nil
}

func Execute(ctx context.Context, catalog Catalog, syncer mainsync.Synchronizer, runtime Runtime, cwd, explicitProject, explicitWorkstream string, dryRun bool) (Plan, LaunchResult, error) {
	plan, err := Build(catalog, cwd, explicitProject, explicitWorkstream)
	if err != nil {
		return Plan{}, LaunchResult{}, err
	}
	if dryRun {
		return plan, LaunchResult{}, nil
	}
	syncResult, err := syncer.Sync(ctx, plan.RootRepository)
	if err != nil {
		return plan, LaunchResult{}, fmt.Errorf("synchronize main before opening planner: %w", err)
	}
	result, err := runtime.Ensure(ctx, plan)
	result.MainWorktree = syncResult.MainWorktree
	if err != nil {
		return plan, result, err
	}
	if result.FXSessionID != "" && result.FXSessionID != plan.Workstream.PlannerSessionID {
		if err := catalog.SetWorkstreamPlannerSession(plan.Workstream, result.FXSessionID); err != nil {
			return plan, result, fmt.Errorf("planner started but workstream session persistence failed: %w", err)
		}
	}
	return plan, result, nil
}

func safeComponent(value string) string {
	value = strings.Trim(unsafeName.ReplaceAllString(value, "-"), "-_")
	if value == "" {
		value = "project"
	}
	if len(value) > 32 {
		value = value[:32]
	}
	return value
}

func sortEntities(entities []workload.Entity) {
	sort.SliceStable(entities, func(i, j int) bool {
		if entities[i].Order > 0 && entities[j].Order > 0 && entities[i].Order != entities[j].Order {
			return entities[i].Order < entities[j].Order
		}
		return entities[i].ID < entities[j].ID
	})
}

func buildPrompt(plan Plan) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "Collaborative planning session context for workstream %s (%s) in canonical project %s (%s).\n\n", plan.Workstream.ID, plan.Workstream.Title, plan.Project.ID, plan.Project.Title)
	builder.WriteString("No planning action is requested by this bootstrap message. For your first response, do not call tools, read files, investigate the repository, change planning metadata, or make recommendations. Briefly acknowledge the workstream context and ask the user what they would like to plan together.\n\n")
	builder.WriteString(plan.Profile.Text)
	builder.WriteByte('\n')
	fmt.Fprintf(&builder, "Project manifest: %s\nWorkstream packet: %s\nTarget repository: %s\n", plan.Project.SourcePath, plan.Workstream.SourcePath, plan.RootRepository)
	builder.WriteString("Repository registry:\n")
	if len(plan.Repositories) == 0 {
		builder.WriteString("- (none)\n")
	}
	for _, repository := range plan.Repositories {
		fmt.Fprintf(&builder, "- %s\n", repository)
	}
	builder.WriteString("Selected workstream:\n")
	if len(plan.Workstreams) == 0 {
		builder.WriteString("- (none)\n")
	}
	for _, workstream := range plan.Workstreams {
		fmt.Fprintf(&builder, "- %s [%s]: %s (%s)\n", workstream.ID, workstream.Status, workstream.Title, workstream.SourcePath)
	}
	builder.WriteString("Open task summary:\n")
	if len(plan.Tasks) == 0 {
		builder.WriteString("- (none)\n")
	}
	for _, task := range plan.Tasks {
		fmt.Fprintf(&builder, "- %s / %s [%s]: %s; repository=%s\n", task.Workstream, task.ID, task.Status, task.Title, task.Repository)
	}
	builder.WriteString("\nAfter the user provides a goal, collaborate on scope, constraints, tradeoffs, and task boundaries. Then use the obsidian-planner skill for durable planning and task packets and fx-workbench list/show/scaffold/status as the canonical workload CLI. Inspect only the metadata and source relevant to the user's stated direction before changing plans. Planning completion alone is not approval to start implementation: do not launch tasks, create branches/worktrees, or modify repository/runtime state automatically. If the user explicitly asks to start a specific task or explicitly approves a proposed launch, you may use fx-workbench launch or the worktree-task-launcher skill to start exactly the approved work. Never infer approval or launch unrelated tasks. Until the user provides initial direction, remain idle and do not act on the context above.")
	return builder.String()
}

func Format(plan Plan, result LaunchResult, dryRun bool) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "project: %s\nworkstream: %s\nmanifest: %s\nworkstream packet: %s\ntarget repository: %s\nsession target: %s\nwindow target: %s\n", plan.Project.ID, plan.Workstream.ID, plan.Project.SourcePath, plan.Workstream.SourcePath, plan.RootRepository, plan.SessionName, plan.WindowName)
	fmt.Fprintf(&builder, "repositories: %s\n", strings.Join(plan.Repositories, ", "))
	fmt.Fprintf(&builder, "agent profile: %s (%s)\n", plan.Profile.ID, plan.Profile.Digest)
	fmt.Fprintf(&builder, "active workstreams: %s\nopen tasks: %s\n", entityIDs(plan.Workstreams), entityIDs(plan.Tasks))
	if dryRun {
		fmt.Fprintln(&builder, "dry-run: planner preflight passed; main and tmux were not modified")
		fmt.Fprintln(&builder, "prompt:")
		builder.WriteString(plan.Prompt)
		builder.WriteByte('\n')
		return builder.String()
	}
	fmt.Fprintf(&builder, "main synchronized: %s\n", result.MainWorktree)
	sessionState, plannerState := "reused", "reused"
	if result.CreatedSession {
		sessionState = "created"
	} else if result.AdoptedSession {
		sessionState = "adopted"
	}
	if result.CreatedPlanner {
		plannerState = "created"
	} else if result.AdoptedPlanner {
		plannerState = "adopted"
	}
	fmt.Fprintf(&builder, "session: %s\nplanner: %s\npane: %s\nfx session: %s\n", sessionState, plannerState, result.PaneID, result.FXSessionID)
	return builder.String()
}

func entityIDs(entities []workload.Entity) string {
	if len(entities) == 0 {
		return "(none)"
	}
	ids := make([]string, len(entities))
	for i, entity := range entities {
		ids[i] = entity.ID
	}
	return strings.Join(ids, ", ")
}

var ErrNoPlanner = errors.New("planner pane not found")
