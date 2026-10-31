package appcli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/jarneson/fx-workbench/internal/cleanup"
	"github.com/jarneson/fx-workbench/internal/mainsync"
	"github.com/jarneson/fx-workbench/internal/planner"
	"github.com/jarneson/fx-workbench/internal/profiles"
	"github.com/jarneson/fx-workbench/internal/projectclose"
	"github.com/jarneson/fx-workbench/internal/tasklaunch"
	"github.com/jarneson/fx-workbench/internal/taskui"
	"github.com/jarneson/fx-workbench/internal/workbench"
	"github.com/jarneson/fx-workbench/internal/workload"
)

func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return -1
	}
	store := workload.NewStore()
	var err error
	switch args[0] {
	case "project":
		err = runProject(store, args[1:], stdout, stderr)
	case "list":
		err = runList(store, args[1:], stdout, stderr)
	case "show":
		err = runShow(store, args[1:], stdout, stderr)
	case "scaffold":
		err = runScaffold(store, args[1:], stdout, stderr)
	case "status":
		err = runStatus(store, args[1:], stdout, stderr)
	case "launch":
		err = runLaunch(store, args[1:], stdout, stderr)
	case "cleanup":
		err = runCleanup(store, args[1:], stdout, stderr)
	case "plan":
		err = runPlan(store, args[1:], stdout, stderr)
	case "root":
		err = runRoot(args[1:], stdout, stderr)
	case "session":
		err = runSession(args[1:], stdout, stderr)
	case "principle":
		err = runPrinciple(args[1:], stdout, stderr)
	case "fx":
		err = runFXAssets(args[1:], stdout, stderr)
	case "review-relay":
		err = taskui.RunReviewRelay(args[1:], stderr)
	case "help", "-h", "--help":
		usage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "fx-workbench: unknown command: %s\n", args[0])
		usage(stderr)
		return 2
	}
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintf(stderr, "fx-workbench: %v\n", err)
		return 1
	}
	return 0
}

func usage(writer io.Writer) {
	fmt.Fprintln(writer, `Usage:
  fx-workbench                         Open the tmux cockpit
  fx-workbench tasks [--project <id>]  Browse durable project workloads
  fx-workbench sidebar --project <id> --workstream <id>
                                      Watch one workstream's tasks
  fx-workbench project onboard [flags] Create, migrate, and select a project
  fx-workbench project migrate-layout  Move canonical workloads to numbered folders
  fx-workbench project close|reopen    Manage project lifecycle
  fx-workbench project resolve         Resolve a project from the current workspace
  fx-workbench list [filters]          List projects, workstreams, and tasks
  fx-workbench show <id-or-path>       Show one normalized workload
  fx-workbench scaffold <type> [flags] Create a canonical project/workstream/task
  fx-workbench status --set <state> <task-or-workstream>
  fx-workbench launch [--dry-run] [--branch <name>] <task>
  fx-workbench cleanup <--dry-run|--yes> [--merged] [task]
  fx-workbench cleanup <--dry-run|--yes> --force --worktree <path>
  fx-workbench cleanup <--dry-run|--yes> --session <id>
  fx-workbench plan [--project <id>] [--workstream <id>] Launch a workstream planner
  fx-workbench root [--repository <path>] Create or reuse a root tmux session
  fx-workbench session <name|tag> [flags] Manage runtime session identity
  fx-workbench principle <command>       List, render, or apply principles
  fx-workbench fx install [flags]         Install Workbench-owned fx assets

Common filters: --type project|workstream|task --project <id> --workstream <id> --status <state> --json`)
}

func runProject(store *workload.Store, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: fx-workbench project <onboard|migrate-layout> [flags]")
	}
	switch args[0] {
	case "onboard":
		return runProjectOnboard(store, args[1:], stdout, stderr)
	case "migrate-layout":
		return runProjectMigrateLayout(store, args[1:], stdout, stderr)
	case "close":
		return runProjectClose(store, args[1:], stdout, stderr)
	case "reopen":
		return runProjectReopen(store, args[1:], stdout, stderr)
	case "resolve":
		return runProjectResolve(store, args[1:], stdout, stderr)
	default:
		return fmt.Errorf("unknown project command: %s", args[0])
	}
}

func runProjectOnboard(store *workload.Store, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("project onboard", flag.ContinueOnError)
	flags.SetOutput(stderr)
	id := flags.String("id", "", "stable project id")
	title := flags.String("title", "", "project title")
	rootRepository := flags.String("root-repository", "", "root planning repository (defaults to cwd's Git root)")
	var repositories stringList
	flags.Var(&repositories, "repo", "participating repository root (repeatable)")
	objective := flags.String("objective", "", "project goal")
	workstreamID := flags.String("workstream-id", "", "initial workstream id (default core or migrated id)")
	workstreamTitle := flags.String("workstream-title", "", "initial workstream title")
	workstreamObjective := flags.String("workstream-objective", "", "initial workstream goal")
	fromProject := flags.String("from-project", "", "source project for migration")
	migrateWorkstream := flags.String("migrate-workstream", "", "canonical workstream id to migrate")
	selectProject := flags.Bool("select", true, "select the project after onboarding")
	dryRun := flags.Bool("dry-run", false, "validate and print the plan without writing")
	jsonOutput := flags.Bool("json", false, "emit JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	if *rootRepository == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		output, err := exec.Command("git", "-C", cwd, "rev-parse", "--show-toplevel").CombinedOutput()
		if err != nil {
			return fmt.Errorf("resolve root repository from cwd: %s", strings.TrimSpace(string(output)))
		}
		*rootRepository = strings.TrimSpace(string(output))
	}
	if *id == "" {
		*id = workload.Slug(*title)
	}
	result, err := store.Onboard(workload.OnboardOptions{
		ID: *id, Title: *title, Objective: *objective,
		RootRepository: *rootRepository, Repositories: repositories,
		WorkstreamID: *workstreamID, WorkstreamTitle: *workstreamTitle,
		WorkstreamObjective: *workstreamObjective,
		FromProject:         *fromProject, MigrateWorkstream: *migrateWorkstream,
		Select: *selectProject, DryRun: *dryRun,
	})
	if err != nil {
		return err
	}
	if *jsonOutput {
		return writeJSON(stdout, result)
	}
	fmt.Fprintf(stdout, "project: %s\nroot repository: %s\n", result.Project.ID, result.Project.RootRepository)
	fmt.Fprintf(stdout, "repositories: %s\nworkstream: %s\n", strings.Join(result.Repositories, ", "), result.Workstream.ID)
	for _, migration := range result.Migrations {
		fmt.Fprintf(stdout, "migrate %s %s: %s -> %s\n", migration.Type, migration.ID, migration.SourcePath, migration.Destination)
	}
	if result.DryRun {
		fmt.Fprintln(stdout, "dry-run: onboarding preflight passed")
	} else {
		fmt.Fprintf(stdout, "manifest: %s\n", result.Project.SourcePath)
		if result.Selected {
			fmt.Fprintln(stdout, "selected: yes")
		}
	}
	return nil
}

func runProjectMigrateLayout(store *workload.Store, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("project migrate-layout", flag.ContinueOnError)
	flags.SetOutput(stderr)
	project := flags.String("project", "", "project id (defaults to current)")
	dryRun := flags.Bool("dry-run", false, "validate and print moves without writing")
	jsonOutput := flags.Bool("json", false, "emit JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	result, err := store.MigrateLayout(*project, *dryRun)
	if err != nil {
		return err
	}
	if *jsonOutput {
		return writeJSON(stdout, result)
	}
	fmt.Fprintf(stdout, "project: %s\n", result.Project)
	for _, move := range result.Moves {
		fmt.Fprintf(stdout, "move %s %s: %s -> %s\n", move.Type, move.ID, move.SourcePath, move.Destination)
	}
	if result.DryRun {
		fmt.Fprintln(stdout, "dry-run: layout migration preflight passed")
	} else {
		fmt.Fprintf(stdout, "migrated: %d workloads\n", len(result.Moves))
	}
	return nil
}

func runProjectResolve(store *workload.Store, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("project resolve", flag.ContinueOnError)
	flags.SetOutput(stderr)
	cwd := flags.String("cwd", "", "workspace path (defaults to cwd)")
	jsonOutput := flags.Bool("json", false, "emit JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	resolution, err := store.ResolveProject(*cwd, "")
	if err != nil {
		return err
	}
	if *jsonOutput {
		return writeJSON(stdout, resolution)
	}
	fmt.Fprintf(stdout, "project: %s\nroot repository: %s\nreason: %s\n", resolution.Project.ID, resolution.Project.RootRepository, resolution.Reason)
	return nil
}

func runProjectClose(store *workload.Store, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("project close", flag.ContinueOnError)
	flags.SetOutput(stderr)
	project := flags.String("project", "", "project id (defaults to current)")
	dryRun := flags.Bool("dry-run", false, "print closeout eligibility without mutating")
	yes := flags.Bool("yes", false, "close an eligible project")
	closeSessions := flags.Bool("close-sessions", false, "close tagged task and root/planner sessions")
	jsonOutput := flags.Bool("json", false, "emit JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *dryRun == *yes {
		return errors.New("choose exactly one of --dry-run or --yes")
	}
	if flags.NArg() > 1 {
		return errors.New("project close accepts at most one project id")
	}
	if flags.NArg() == 1 {
		if *project != "" {
			return errors.New("project id provided twice")
		}
		*project = flags.Arg(0)
	}
	runtime := cleanup.NewService()
	plan, err := projectclose.Build(context.Background(), store, runtime, *project, *closeSessions)
	if err != nil {
		return err
	}
	if *jsonOutput && *dryRun {
		return writeJSON(stdout, plan)
	}
	state := "eligible"
	if !plan.Eligible {
		state = "blocked"
	}
	fmt.Fprintf(stdout, "project: %s\nstatus: %s\nworkstreams: %d\ntasks: %d\n", plan.Project.ID, state, len(plan.Workstreams), len(plan.Tasks))
	for _, worktree := range plan.Worktrees {
		fmt.Fprintf(stdout, "worktree: %s (%s)\n", worktree.Path, worktree.TaskID)
	}
	for _, session := range plan.Sessions {
		fmt.Fprintf(stdout, "session: %s (%s)\n", session.Name, session.ID)
	}
	for _, blocker := range plan.Blockers {
		fmt.Fprintf(stdout, "blocker: %s\n", blocker)
	}
	if *dryRun {
		return nil
	}
	result, err := projectclose.Execute(context.Background(), store, runtime, plan)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "closed: %t\nsessions closed: %d\n", result.Closed, result.SessionsClosed)
	return nil
}

func runProjectReopen(store *workload.Store, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("project reopen", flag.ContinueOnError)
	flags.SetOutput(stderr)
	project := flags.String("project", "", "project id")
	dryRun := flags.Bool("dry-run", false, "validate without mutating")
	yes := flags.Bool("yes", false, "reopen the project")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *dryRun == *yes {
		return errors.New("choose exactly one of --dry-run or --yes")
	}
	if flags.NArg() > 1 {
		return errors.New("project reopen accepts one project id")
	}
	if flags.NArg() == 1 {
		if *project != "" {
			return errors.New("project id provided twice")
		}
		*project = flags.Arg(0)
	}
	if *project == "" {
		return errors.New("project reopen requires a project id")
	}
	entity, err := store.FindType(*project, *project, "project")
	if err != nil {
		return err
	}
	if entity.Status != "closed" {
		return fmt.Errorf("project %s is %q, not closed", entity.ID, entity.Status)
	}
	fmt.Fprintf(stdout, "project: %s\nstatus: closed\n", entity.ID)
	if *dryRun {
		return nil
	}
	if err := projectclose.Reopen(store, *project); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "reopened: true")
	return nil
}

func runList(store *workload.Store, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("list", flag.ContinueOnError)
	flags.SetOutput(stderr)
	kind := flags.String("type", "", "entity type")
	project := flags.String("project", "", "project id")
	workstream := flags.String("workstream", "", "workstream id")
	status := flags.String("status", "", "workflow status")
	jsonOutput := flags.Bool("json", false, "emit JSON")
	all := flags.Bool("all", false, "include closed projects")
	allProjects := flags.Bool("all-projects", false, "list across every active project")
	if err := flags.Parse(args); err != nil {
		return err
	}
	entities, err := store.List(workload.ListOptions{Type: *kind, Project: *project, Workstream: *workstream, Status: *status, IncludeClosed: *all, AllProjects: *allProjects})
	if err != nil {
		return err
	}
	if *jsonOutput {
		return writeJSON(stdout, entities)
	}
	writer := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "TYPE\t#\tID\tSTATUS\tPROJECT / WORKSTREAM\tREPOSITORY\tNOTES")
	for _, entity := range entities {
		parent := entity.Project
		if entity.Workstream != "" {
			parent += " / " + entity.Workstream
		}
		notes := strings.Join(entity.Diagnostics, "; ")
		repository := entity.Repository
		if entity.Type == "project" {
			repository = entity.RootRepository
		}
		order := ""
		if entity.Order > 0 {
			order = fmt.Sprintf("%02d", entity.Order)
		}
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", entity.Type, order, entity.ID, entity.Status, parent, repository, notes)
	}
	return writer.Flush()
}

func runShow(store *workload.Store, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("show", flag.ContinueOnError)
	flags.SetOutput(stderr)
	project := flags.String("project", "", "project id")
	jsonOutput := flags.Bool("json", false, "emit JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	remaining := flags.Args()
	var refs []string
	for i := 0; i < len(remaining); i++ {
		switch remaining[i] {
		case "--json":
			*jsonOutput = true
		case "--project":
			if i+1 >= len(remaining) {
				return errors.New("--project requires a value")
			}
			i++
			*project = remaining[i]
		default:
			refs = append(refs, remaining[i])
		}
	}
	if len(refs) != 1 {
		return errors.New("show requires one id or path")
	}
	entity, err := store.Find(refs[0], *project)
	if err != nil {
		return err
	}
	if *jsonOutput {
		return writeJSON(stdout, entity)
	}
	fmt.Fprintf(stdout, "%s %s — %s\nstatus: %s\nproject: %s\n", entity.Type, entity.ID, entity.Title, entity.Status, entity.Project)
	if entity.Workstream != "" {
		fmt.Fprintf(stdout, "workstream: %s\n", entity.Workstream)
	}
	if entity.RootRepository != "" {
		fmt.Fprintf(stdout, "root repository: %s\n", entity.RootRepository)
	}
	if len(entity.Repositories) > 0 {
		fmt.Fprintf(stdout, "repositories: %s\n", strings.Join(entity.Repositories, ", "))
	}
	if entity.Repository != "" {
		fmt.Fprintf(stdout, "repository: %s\n", entity.Repository)
	}
	if entity.Branch != "" {
		fmt.Fprintf(stdout, "branch: %s\n", entity.Branch)
	}
	fmt.Fprintf(stdout, "source: %s\n", entity.SourcePath)
	for _, diagnostic := range entity.Diagnostics {
		fmt.Fprintf(stdout, "warning: %s\n", diagnostic)
	}
	return nil
}

func runScaffold(store *workload.Store, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("scaffold requires project, workstream, or task")
	}
	kind := args[0]
	flags := flag.NewFlagSet("scaffold "+kind, flag.ContinueOnError)
	flags.SetOutput(stderr)
	id := flags.String("id", "", "stable kebab-case id")
	title := flags.String("title", "", "display title")
	project := flags.String("project", "", "project id (defaults to current)")
	workstream := flags.String("workstream", "", "workstream id")
	rootRepository := flags.String("root-repository", "", "project planning repository")
	var repositories stringList
	flags.Var(&repositories, "repo", "project repository path (repeatable)")
	repository := flags.String("repository", "", "absolute task/workstream repository path")
	branch := flags.String("branch", "", "default branch name for launch")
	var dependencies stringList
	flags.Var(&dependencies, "depends-on", "dependency task id (repeatable)")
	status := flags.String("status", "draft", "initial status")
	objective := flags.String("objective", "", "initial goal or objective")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if *project == "" && kind != "project" {
		current, err := workload.CurrentProject()
		if err != nil {
			return err
		}
		*project = current
	}
	if *id == "" {
		*id = workload.Slug(*title)
	}
	entity, err := store.Scaffold(workload.Entity{
		Type: kind, ID: *id, Title: *title, Status: *status, Project: *project,
		RootRepository: *rootRepository, Repositories: repositories,
		Workstream: *workstream, Repository: *repository, Branch: *branch, DependsOn: dependencies,
	}, *objective)
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, entity.SourcePath)
	return nil
}

func runStatus(store *workload.Store, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("status", flag.ContinueOnError)
	flags.SetOutput(stderr)
	project := flags.String("project", "", "project id")
	status := flags.String("set", "", "new status")
	fxSessionID := flags.String("fx-session-id", "", "persist an fx session ID when activating a task")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *status == "" || flags.NArg() != 1 {
		return errors.New("usage: fx-workbench status --set <state> <task-or-workstream>")
	}
	entity, err := store.Find(flags.Arg(0), *project)
	if err != nil {
		return err
	}
	if *fxSessionID != "" && entity.Type != "task" {
		return errors.New("--fx-session-id is only valid for tasks")
	}
	switch entity.Type {
	case "task":
		if !workload.ValidTaskStatus(*status) {
			return fmt.Errorf("invalid task status: %s", *status)
		}
		if *fxSessionID != "" {
			if *status != "coding" {
				return errors.New("--fx-session-id requires --set coding")
			}
			if err := store.SetTaskLaunch(entity, *fxSessionID); err != nil {
				return err
			}
		} else if err := store.SetStatus(entity, *status); err != nil {
			return err
		}
	case "workstream":
		if *status != "closed" && *status != "active" {
			return fmt.Errorf("workstream status must be active or closed, got %s", *status)
		}
		if *status == "closed" {
			tasks, listErr := store.List(workload.ListOptions{Type: "task", Project: entity.Project, Workstream: entity.ID, IncludeClosed: true})
			if listErr != nil {
				return listErr
			}
			counts := make(map[string]int)
			for _, task := range tasks {
				if task.Status != "merged" && task.Status != "superseded" {
					counts[task.Status]++
				}
			}
			if len(counts) > 0 {
				var blockers []string
				for state, count := range counts {
					blockers = append(blockers, fmt.Sprintf("%s=%d", state, count))
				}
				sort.Strings(blockers)
				return fmt.Errorf("workstream has nonterminal tasks: %s", strings.Join(blockers, ", "))
			}
		}
		if err := store.SetWorkstreamClosed(entity, *status == "closed"); err != nil {
			return err
		}
	case "project":
		return errors.New("use fx-workbench project close|reopen for project lifecycle")
	default:
		return fmt.Errorf("status target type is unsupported: %s", entity.Type)
	}
	fmt.Fprintf(stdout, "%s: %s\n", entity.ID, *status)
	return nil
}

func runLaunch(store *workload.Store, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("launch", flag.ContinueOnError)
	flags.SetOutput(stderr)
	project := flags.String("project", "", "project id")
	branch := flags.String("branch", "", "branch name (defaults to task frontmatter)")
	dryRun := flags.Bool("dry-run", false, "validate without creating resources")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("launch requires one task id or path")
	}
	entity, err := store.Find(flags.Arg(0), *project)
	if err != nil {
		return err
	}
	if entity.Type != "task" {
		return fmt.Errorf("launch target is not a task: %s", entity.ID)
	}
	if entity.Status != "ready" {
		return fmt.Errorf("task must be ready to launch; current status is %q", entity.Status)
	}
	for _, dependencyID := range entity.DependsOn {
		dependency, err := store.Find(dependencyID, entity.Project)
		if err != nil {
			return fmt.Errorf("resolve dependency %s: %w", dependencyID, err)
		}
		if !workload.DependencySatisfied(dependency.Status) {
			return fmt.Errorf("dependency %s is %q, not merged", dependency.ID, dependency.Status)
		}
	}
	projectEntity, err := store.Find(entity.Project, entity.Project)
	if err == nil && len(projectEntity.Repositories) > 0 && !contains(projectEntity.Repositories, entity.Repository) {
		return fmt.Errorf("task repository is not registered by project %s: %s", entity.Project, entity.Repository)
	}
	if *branch == "" {
		*branch = entity.Branch
	}
	if *branch == "" {
		return errors.New("branch is required via --branch or task frontmatter")
	}
	if !filepath.IsAbs(entity.Repository) {
		return fmt.Errorf("task repository must be an absolute path: %q", entity.Repository)
	}
	if info, err := os.Stat(entity.Repository); err != nil || !info.IsDir() {
		return fmt.Errorf("task repository does not exist: %s", entity.Repository)
	}
	if output, err := exec.Command("git", "-C", entity.Repository, "rev-parse", "--show-toplevel").CombinedOutput(); err != nil {
		return fmt.Errorf("task repository is not a git checkout: %s", strings.TrimSpace(string(output)))
	}
	if output, err := exec.Command("git", "check-ref-format", "--branch", *branch).CombinedOutput(); err != nil {
		return fmt.Errorf("invalid branch %q: %s", *branch, strings.TrimSpace(string(output)))
	}
	profile, err := profiles.Load("coding")
	if err != nil {
		return fmt.Errorf("load coding agent profile: %w", err)
	}
	fmt.Fprintf(stdout, "task: %s\nrepository: %s\nbranch: %s\npacket: %s\nagent profile: %s (%s)\n", entity.ID, entity.Repository, *branch, entity.SourcePath, profile.ID, profile.Digest)
	if *dryRun {
		fmt.Fprintln(stdout, "dry-run: launch preflight passed")
		return nil
	}
	service := tasklaunch.NewService(tasklaunch.NewRuntime())
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	result, err := service.Launch(ctx, entity, *branch, func(progress tasklaunch.Progress) {
		fmt.Fprintln(stdout, tasklaunch.ProgressLine(progress))
	})
	if err != nil {
		return fmt.Errorf("task launch failed: %w", err)
	}
	if err := store.SetTaskLaunch(entity, result.FXSessionID); err != nil {
		return fmt.Errorf("task started but launch metadata update failed: %w", err)
	}
	fmt.Fprintf(stdout, "Started task in %s\ntmux session: %s\ntmux window: %s\nfx pane: %s\nfx session: %s\n", result.Worktree, result.Session, result.FXWindow, result.FXPane, result.FXSessionID)
	return nil
}

func runCleanup(store *workload.Store, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("cleanup", flag.ContinueOnError)
	flags.SetOutput(stderr)
	project := flags.String("project", "", "project id (defaults to current)")
	merged := flags.Bool("merged", false, "consider every task with a remaining workspace")
	worktrees := flags.Bool("worktrees", false, "fully validate every non-root worktree in the current Git repository")
	inventory := flags.Bool("inventory", false, "quickly list non-root worktrees without expensive validation")
	force := flags.Bool("force", false, "override soft cleanup blockers for one exact worktree")
	worktreePath := flags.String("worktree", "", "exact worktree path for forced cleanup")
	sessionTarget := flags.String("session", "", "orphaned task session id or name")
	dryRun := flags.Bool("dry-run", false, "print eligibility and actions without mutating")
	yes := flags.Bool("yes", false, "execute eligible cleanup plans")
	trustMerged := flags.Bool("trust-merged", false, "trust merged status when integration cannot be mechanically verified")
	trustDone := flags.Bool("trust-done", false, "deprecated alias for --trust-merged")
	deleteBranch := flags.Bool("delete-branch", false, "delete the merged local branch after removing its worktree")
	jsonOutput := flags.Bool("json", false, "emit JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *dryRun == *yes {
		return errors.New("choose exactly one of --dry-run or --yes")
	}
	service := cleanup.NewService()
	if *sessionTarget != "" {
		if *force || *worktreePath != "" || *merged || *worktrees || *inventory || flags.NArg() != 0 {
			return errors.New("--session accepts no task id or other cleanup mode")
		}
		plan := service.PlanSessionCleanup(context.Background(), *sessionTarget)
		if *jsonOutput && *dryRun {
			return writeJSON(stdout, plan)
		}
		state := "eligible"
		if !plan.Eligible {
			state = "blocked"
		}
		fmt.Fprintf(stdout, "session cleanup: %s\nsession: %s (%s)\nworktree: %s\n", state, plan.Session.Name, plan.Session.ID, plan.Session.Worktree)
		for _, warning := range plan.Warnings {
			fmt.Fprintf(stdout, "warning: %s\n", warning)
		}
		for _, blocker := range plan.Blockers {
			fmt.Fprintf(stdout, "blocker: %s\n", blocker)
		}
		if *dryRun {
			return nil
		}
		return service.ExecuteSessionCleanup(context.Background(), plan)
	}
	if *force {
		if *worktreePath == "" || *merged || *worktrees || *inventory || flags.NArg() != 0 {
			return errors.New("--force requires --worktree <path> and accepts no task id, --merged, --worktrees, or --inventory")
		}
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		repository, err := repositoryRoot(cwd)
		if err != nil {
			return err
		}
		plan := service.PlanForce(context.Background(), repository, *worktreePath)
		if *jsonOutput && *dryRun {
			return writeJSON(stdout, plan)
		}
		state := "eligible"
		if !plan.Eligible {
			state = "blocked"
		}
		fmt.Fprintf(stdout, "forced cleanup: %s\nbranch: %s\nworktree: %s\n", state, plan.Branch, plan.Worktree)
		for _, change := range plan.Changes {
			fmt.Fprintf(stdout, "discard: %s\n", change)
		}
		for _, warning := range plan.Warnings {
			fmt.Fprintf(stdout, "warning: %s\n", warning)
		}
		for _, blocker := range plan.Blockers {
			fmt.Fprintf(stdout, "blocker: %s\n", blocker)
		}
		if *dryRun {
			return nil
		}
		result, err := service.ExecuteForce(context.Background(), plan, *deleteBranch)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "removed: worktree=%t sessions=%d windows=%d branch=%t\n", result.WorktreeRemoved, result.SessionsKilled, result.WindowsKilled, result.BranchDeleted)
		return nil
	}
	if *worktreePath != "" {
		return errors.New("--worktree is only valid with --force")
	}
	if *worktrees || *inventory {
		if *worktrees && *inventory {
			return errors.New("choose only one of --worktrees or --inventory")
		}
		if !*dryRun || *merged || flags.NArg() != 0 {
			return errors.New("--worktrees/--inventory require --dry-run and accept no task id or --merged")
		}
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		resolution, err := store.ResolveProject(cwd, *project)
		if err != nil {
			return err
		}
		tasks, err := store.List(workload.ListOptions{Type: "task", Project: resolution.Project.ID})
		if err != nil {
			return err
		}
		repository, err := repositoryRoot(cwd)
		if err != nil {
			return err
		}
		if *inventory {
			entries, inventoryErr := service.Inventory(context.Background(), repository, tasks)
			if inventoryErr != nil {
				return inventoryErr
			}
			if *jsonOutput {
				return writeJSON(stdout, entries)
			}
			if len(entries) == 0 {
				fmt.Fprintln(stdout, "No non-root worktrees found.")
				return nil
			}
			for _, entry := range entries {
				fmt.Fprintf(stdout, "category: %s\nbranch: %s\nworktree: %s\ntask: %s\n", entry.Category, entry.Branch, entry.Path, entry.TaskID)
				for _, reason := range entry.Reasons {
					fmt.Fprintf(stdout, "reason: %s\n", reason)
				}
			}
			return nil
		}
		scan, err := service.ScanWorktrees(context.Background(), repository, tasks)
		if err != nil {
			return err
		}
		if *jsonOutput {
			return writeJSON(stdout, scan)
		}
		if len(scan) == 0 {
			fmt.Fprintln(stdout, "No non-root worktrees found.")
			return nil
		}
		for _, entry := range scan {
			fmt.Fprintf(stdout, "category: %s\nbranch: %s\nworktree: %s\ntask: %s\n", entry.Category, entry.Branch, entry.Path, entry.TaskID)
			for _, blocker := range entry.Plan.Blockers {
				fmt.Fprintf(stdout, "blocker: %s\n", blocker)
			}
		}
		return nil
	}
	if *merged && flags.NArg() != 0 {
		return errors.New("--merged does not accept a task id")
	}
	if !*merged && flags.NArg() != 1 {
		return errors.New("cleanup requires one task id or --merged")
	}
	var tasks []workload.Entity
	if *merged {
		listed, err := store.List(workload.ListOptions{Type: "task", Project: *project})
		if err != nil {
			return err
		}
		tasks = listed
	} else {
		task, err := store.Find(flags.Arg(0), *project)
		if err != nil {
			return err
		}
		tasks = []workload.Entity{task}
	}
	plans := make([]cleanup.Plan, 0, len(tasks))
	for _, task := range tasks {
		plan := service.PlanWithOptions(context.Background(), task, cleanup.PlanOptions{TrustMerged: *trustMerged || *trustDone})
		if *merged && plan.Worktree == "" && len(plan.Sessions) == 0 {
			continue
		}
		plans = append(plans, plan)
	}
	if *jsonOutput && *dryRun {
		return writeJSON(stdout, plans)
	}
	if len(plans) == 0 {
		fmt.Fprintln(stdout, "No task workspaces found.")
		return nil
	}
	blocked := false
	for _, plan := range plans {
		state := "eligible"
		if !plan.Eligible {
			state = "blocked"
			blocked = true
		}
		fmt.Fprintf(stdout, "task: %s\nstatus: %s\nbranch: %s\nworktree: %s\n", plan.Task.ID, state, plan.Branch, plan.Worktree)
		for _, session := range plan.Sessions {
			if session.WindowID != "" {
				fmt.Fprintf(stdout, "window: %s:%s (%s)\n", session.Name, session.WindowName, session.WindowID)
			} else {
				fmt.Fprintf(stdout, "session: %s (%s)\n", session.Name, session.ID)
			}
		}
		for _, blocker := range plan.Blockers {
			fmt.Fprintf(stdout, "blocker: %s\n", blocker)
		}
		if *dryRun {
			continue
		}
		if !plan.Eligible {
			continue
		}
		result, err := service.Execute(context.Background(), plan, *deleteBranch)
		if err != nil {
			return fmt.Errorf("cleanup %s: %w", plan.Task.ID, err)
		}
		fmt.Fprintf(stdout, "removed: worktree=%t sessions=%d windows=%d branch=%t\n", result.WorktreeRemoved, result.SessionsKilled, result.WindowsKilled, result.BranchDeleted)
	}
	if !*merged && blocked {
		return errors.New("cleanup plan is blocked")
	}
	return nil
}

func runPlan(store *workload.Store, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("plan", flag.ContinueOnError)
	flags.SetOutput(stderr)
	project := flags.String("project", "", "project id (defaults to workspace resolution)")
	workstream := flags.String("workstream", "", "workstream id (required when the project has multiple active workstreams)")
	dryRun := flags.Bool("dry-run", false, "validate and print the planner inputs without inspecting or modifying tmux")
	tmuxBinary := flags.String("tmux-bin", "", "tmux executable")
	fxCommand := flags.String("fx-command", "", "fx-tmux executable")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	runtime := planner.NewTmuxRuntime(*tmuxBinary, *fxCommand)
	plan, result, err := planner.Execute(context.Background(), store, mainsync.New(), runtime, cwd, *project, *workstream, *dryRun)
	if err != nil {
		return err
	}
	_, err = io.WriteString(stdout, planner.Format(plan, result, *dryRun))
	return err
}

func runRoot(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("root", flag.ContinueOnError)
	flags.SetOutput(stderr)
	repository := flags.String("repository", "", "repository root (defaults to the current Git repository)")
	tmuxBinary := flags.String("tmux-bin", "", "tmux executable")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	if *repository == "" {
		resolved, err := repositoryRoot("")
		if err != nil {
			return err
		}
		*repository = resolved
	}
	source := workbench.NewTmuxSource(*tmuxBinary)
	result, err := source.EnsureRoot(context.Background(), *repository)
	if err != nil {
		return err
	}
	state := "reused"
	if result.Created {
		state = "created"
	}
	fmt.Fprintf(stdout, "main synchronized: %s\nroot session: %s\nworkspace: %s\nrepository: %s\nstatus: %s\n", result.MainWorktree, result.Name, result.Identity.WorkspaceID, result.Identity.Repository, state)
	return nil
}

func runSession(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: fx-workbench session <name|tag> [flags]")
	}
	flags := flag.NewFlagSet("session "+args[0], flag.ContinueOnError)
	flags.SetOutput(stderr)
	role := flags.String("role", "", "root, task, or workspace")
	repository := flags.String("repository", "", "canonical repository root")
	worktree := flags.String("worktree", "", "worktree or workspace path")
	taskID := flags.String("task-id", "", "durable task id")
	target := flags.String("target", "", "tmux session target")
	tmuxBinary := flags.String("tmux-bin", "", "tmux executable")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	identity, err := workbench.NewIdentity(*role, *repository, *worktree, *taskID)
	if err != nil {
		return err
	}
	switch args[0] {
	case "name":
		fmt.Fprintln(stdout, workbench.SessionName(identity))
		return nil
	case "tag":
		if *target == "" {
			return errors.New("session tag requires --target")
		}
		return workbench.NewTmuxSource(*tmuxBinary).TagSession(context.Background(), *target, identity)
	default:
		return fmt.Errorf("unknown session command: %s", args[0])
	}
}

func repositoryRoot(directory string) (string, error) {
	if directory == "" {
		var err error
		directory, err = os.Getwd()
		if err != nil {
			return "", err
		}
	}
	output, err := exec.Command("git", "-C", directory, "rev-parse", "--path-format=absolute", "--git-common-dir").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("resolve repository root: %s", strings.TrimSpace(string(output)))
	}
	commonDirectory := strings.TrimSpace(string(output))
	if filepath.Base(commonDirectory) == ".git" {
		return filepath.Dir(commonDirectory), nil
	}
	return directory, nil
}

func writeJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

type stringList []string

func (values *stringList) String() string { return strings.Join(*values, ",") }

func (values *stringList) Set(value string) error {
	*values = append(*values, value)
	return nil
}
