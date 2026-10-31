package taskui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/jarneson/fx-workbench/internal/tasklaunch"
	runtimeworkbench "github.com/jarneson/fx-workbench/internal/workbench"
	"github.com/jarneson/fx-workbench/internal/workload"
)

var (
	ink       = lipgloss.Color("#E8E7F0")
	muted     = lipgloss.Color("#77738A")
	purple    = lipgloss.Color("#A78BFA")
	cyan      = lipgloss.Color("#67E8F9")
	green     = lipgloss.Color("#4ADE80")
	yellow    = lipgloss.Color("#FACC15")
	coral     = lipgloss.Color("#FB7185")
	panel     = lipgloss.Color("#24212E")
	selection = lipgloss.Color("#312E45")

	logoStyle  = lipgloss.NewStyle().Bold(true).Foreground(purple)
	mutedStyle = lipgloss.NewStyle().Foreground(muted)
	errorStyle = lipgloss.NewStyle().Foreground(coral)
)

type Catalog interface {
	List(workload.ListOptions) ([]workload.Entity, error)
}

type Runtime interface {
	Snapshot(context.Context) (runtimeworkbench.Snapshot, error)
	Focus(context.Context, runtimeworkbench.Session) error
}

type Actions interface {
	OpenPlanner(context.Context, workload.Entity) (string, error)
	Inspect(context.Context, workload.Entity) (string, error)
	Preflight(context.Context, workload.Entity) (string, error)
	Launch(context.Context, workload.Entity) (string, error)
}

type level int

const (
	projectsLevel level = iota
	workstreamsLevel
	tasksLevel
)

type catalogMsg struct {
	projects, workstreams, tasks []workload.Entity
	snapshot                     runtimeworkbench.Snapshot
	runtimeErr                   error
	err                          error
}

type actionKind string

const (
	actionPlanner   actionKind = "planner"
	actionInspect   actionKind = "inspect"
	actionPreflight actionKind = "preflight"
	actionLaunch    actionKind = "launch"
)

type actionMsg struct {
	kind   actionKind
	output string
	err    error
}

type focusMsg struct {
	taskID string
	err    error
}

type launchMsg struct {
	progress tasklaunch.Progress
	result   tasklaunch.Result
	err      error
	done     bool
}

type TaskLauncher interface {
	Launch(context.Context, workload.Entity, string, func(tasklaunch.Progress)) (tasklaunch.Result, error)
}

type Model struct {
	catalog           Catalog
	actions           Actions
	runtime           Runtime
	launcher          TaskLauncher
	projectOnly       string
	initialPositioned bool
	projects          []workload.Entity
	workstreams       []workload.Entity
	tasks             []workload.Entity
	snapshot          runtimeworkbench.Snapshot
	level             level
	project           int
	workstream        int
	task              int
	width             int
	height            int
	loading           bool
	busy              bool
	confirm           bool
	launching         bool
	launchEvents      <-chan launchMsg
	launchCancel      context.CancelFunc
	launchProgress    []tasklaunch.Progress
	err               error
	runtimeErr        error
	notice            string
}

func NewModel(catalog Catalog, actions Actions, runtime Runtime, project string) Model {
	return NewModelWithLauncher(catalog, actions, runtime, project, nil)
}

func NewModelWithLauncher(catalog Catalog, actions Actions, runtime Runtime, project string, launcher TaskLauncher) Model {
	return Model{catalog: catalog, actions: actions, runtime: runtime, launcher: launcher, projectOnly: project, loading: true}
}

func (m Model) Init() tea.Cmd { return m.load() }

func (m Model) load() tea.Cmd {
	return func() tea.Msg {
		projects, err := m.catalog.List(workload.ListOptions{Type: "project", Project: m.projectOnly})
		if err != nil {
			return catalogMsg{err: err}
		}
		var workstreams, tasks []workload.Entity
		for _, project := range projects {
			loaded, loadErr := m.catalog.List(workload.ListOptions{Project: project.ID})
			if loadErr != nil {
				return catalogMsg{err: loadErr}
			}
			for _, entity := range loaded {
				switch entity.Type {
				case "workstream":
					workstreams = append(workstreams, entity)
				case "task":
					tasks = append(tasks, entity)
				}
			}
		}
		var snapshot runtimeworkbench.Snapshot
		var runtimeErr error
		if m.runtime != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
			defer cancel()
			snapshot, runtimeErr = m.runtime.Snapshot(ctx)
			if runtimeErr != nil {
				runtimeErr = fmt.Errorf("runtime activity unavailable: %w", runtimeErr)
			}
		}
		return catalogMsg{projects: projects, workstreams: workstreams, tasks: tasks, snapshot: snapshot, runtimeErr: runtimeErr}
	}
}

func (m Model) runAction(kind actionKind, entity workload.Entity) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		var output string
		var err error
		switch kind {
		case actionPlanner:
			output, err = m.actions.OpenPlanner(ctx, entity)
		case actionInspect:
			output, err = m.actions.Inspect(ctx, entity)
		case actionPreflight:
			output, err = m.actions.Preflight(ctx, entity)
		case actionLaunch:
			output, err = m.actions.Launch(ctx, entity)
		}
		return actionMsg{kind: kind, output: strings.TrimSpace(output), err: err}
	}
}

func (m Model) focusTask(task workload.Entity, session runtimeworkbench.Session) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
		defer cancel()
		return focusMsg{taskID: task.ID, err: m.runtime.Focus(ctx, session)}
	}
}

func (m *Model) startLaunch(task workload.Entity) tea.Cmd {
	ctx, cancel := context.WithCancel(context.Background())
	events := make(chan launchMsg, 32)
	m.launching = true
	m.launchCancel = cancel
	m.launchEvents = events
	m.launchProgress = nil
	return func() tea.Msg {
		go func() {
			result, err := m.launcher.Launch(ctx, task, task.Branch, func(progress tasklaunch.Progress) {
				events <- launchMsg{progress: progress}
			})
			events <- launchMsg{result: result, err: err, done: true}
		}()
		return <-events
	}
}

func (m Model) waitLaunch() tea.Cmd {
	return func() tea.Msg { return <-m.launchEvents }
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case catalogMsg:
		m.loading = false
		m.err = msg.err
		m.runtimeErr = msg.runtimeErr
		if msg.err == nil {
			m.projects, m.workstreams, m.tasks, m.snapshot = msg.projects, msg.workstreams, msg.tasks, msg.snapshot
			m.clamp()
			if !m.initialPositioned {
				m.initialPositioned = true
				if m.projectOnly != "" && len(m.projects) == 1 {
					m.level = workstreamsLevel
					m.project, m.workstream, m.task = 0, 0, 0
					if len(m.visibleWorkstreams()) == 1 {
						m.level = tasksLevel
					}
				}
			}
		}
	case focusMsg:
		m.busy = false
		m.err = msg.err
		if msg.err != nil {
			m.notice = "focus failed for " + msg.taskID
			return m, nil
		}
		return m, tea.Quit
	case actionMsg:
		m.busy = false
		m.err = msg.err
		m.notice = msg.output
		if msg.err == nil && m.notice == "" {
			m.notice = string(msg.kind) + " completed"
		}
		if msg.kind == actionPlanner && msg.err == nil {
			return m, tea.Quit
		}
		if msg.kind == actionLaunch && msg.err == nil {
			m.loading = true
			return m, m.load()
		}
	case launchMsg:
		if msg.done {
			m.busy, m.launching = false, false
			m.launchCancel = nil
			m.err = msg.err
			if msg.err == nil {
				m.notice = fmt.Sprintf("Started task in %s · tmux %s · pane %s", msg.result.Worktree, msg.result.Session, msg.result.FXPane)
				m.loading = true
				return m, m.load()
			}
			return m, nil
		}
		m.launchProgress = append(m.launchProgress, msg.progress)
		m.notice = tasklaunch.ProgressLine(msg.progress)
		return m, m.waitLaunch()
	case tea.KeyMsg:
		if m.launching {
			switch msg.String() {
			case "esc":
				m.launchCancel()
				m.notice = "cancelling launch…"
				return m, nil
			case "q", "ctrl+c":
				m.launchCancel()
				return m, tea.Quit
			default:
				return m, nil
			}
		}
		if m.busy {
			return m, nil
		}
		if m.confirm {
			switch msg.String() {
			case "y", "Y":
				task, ok := m.selectedTask()
				if ok {
					m.confirm, m.busy, m.err, m.notice = false, true, nil, "launching "+task.ID+"…"
					if m.launcher != nil {
						return m, m.startLaunch(task)
					}
					return m, m.runAction(actionLaunch, task)
				}
			case "n", "N", "esc":
				m.confirm = false
				m.notice = "launch cancelled"
			}
			return m, nil
		}
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "esc", "backspace", "left", "h":
			if m.level > projectsLevel {
				m.level--
			} else if msg.String() == "esc" {
				return m, tea.Quit
			}
		case "enter":
			if m.level == projectsLevel && len(m.projects) > 0 {
				m.level = workstreamsLevel
				m.workstream = 0
			} else if m.level == workstreamsLevel && len(m.visibleWorkstreams()) > 0 {
				m.level = tasksLevel
				m.task = 0
			} else if task, ok := m.selectedTask(); ok {
				if session, live := m.liveSession(task); live && m.runtime != nil {
					m.busy, m.err, m.notice = true, nil, "focusing "+task.ID+"…"
					return m, m.focusTask(task, session)
				}
				m.notice = "no live tmux workspace for " + task.ID
			}
		case "right", "l":
			if m.level == projectsLevel && len(m.projects) > 0 {
				m.level = workstreamsLevel
				m.workstream = 0
			} else if m.level == workstreamsLevel && len(m.visibleWorkstreams()) > 0 {
				m.level = tasksLevel
				m.task = 0
			}
		case "j", "down":
			m.move(1)
		case "k", "up":
			m.move(-1)
		case "g", "home":
			m.setCursor(0)
		case "G", "end":
			m.setCursor(max(0, len(m.items())-1))
		case "r":
			m.loading, m.err, m.notice = true, nil, ""
			return m, m.load()
		case "p":
			if m.level == projectsLevel {
				m.notice = "select a workstream before opening its planner"
			} else if workstream, ok := m.selectedWorkstream(); ok && m.actions != nil {
				m.busy, m.err, m.notice = true, nil, "opening planner for "+workstream.ID+"…"
				return m, m.runAction(actionPlanner, workstream)
			}
		case "i":
			if entity, ok := m.selectedEntity(); ok && m.actions != nil {
				m.busy, m.err, m.notice = true, nil, "opening source…"
				return m, m.runAction(actionInspect, entity)
			}
		case "c":
			if task, ok := m.selectedTask(); ok && m.actions != nil {
				m.busy, m.err, m.notice = true, nil, "checking "+task.ID+"…"
				return m, m.runAction(actionPreflight, task)
			}
		case "L":
			if task, ok := m.selectedTask(); ok {
				if reason := m.launchBlocker(task); reason != "" {
					m.err = fmt.Errorf("cannot launch %s: %s", task.ID, reason)
				} else {
					m.confirm, m.err = true, nil
				}
			}
		}
	}
	return m, nil
}

func (m *Model) move(delta int) { m.setCursor(max(0, min(len(m.items())-1, m.cursor()+delta))) }
func (m Model) cursor() int {
	switch m.level {
	case projectsLevel:
		return m.project
	case workstreamsLevel:
		return m.workstream
	default:
		return m.task
	}
}
func (m *Model) setCursor(value int) {
	switch m.level {
	case projectsLevel:
		m.project = value
	case workstreamsLevel:
		m.workstream = value
	default:
		m.task = value
	}
}
func (m Model) items() []workload.Entity {
	switch m.level {
	case projectsLevel:
		return m.projects
	case workstreamsLevel:
		return m.visibleWorkstreams()
	default:
		return m.visibleTasks()
	}
}

func (m *Model) clamp() {
	m.project = max(0, min(m.project, len(m.projects)-1))
	m.workstream = max(0, min(m.workstream, len(m.visibleWorkstreams())-1))
	m.task = max(0, min(m.task, len(m.visibleTasks())-1))
}

func (m Model) selectedProject() (workload.Entity, bool) {
	if len(m.projects) == 0 || m.project >= len(m.projects) {
		return workload.Entity{}, false
	}
	return m.projects[m.project], true
}
func (m Model) visibleWorkstreams() []workload.Entity {
	project, ok := m.selectedProject()
	if !ok {
		return nil
	}
	var result []workload.Entity
	for _, entity := range m.workstreams {
		if entity.Project == project.ID {
			result = append(result, entity)
		}
	}
	return result
}
func (m Model) selectedWorkstream() (workload.Entity, bool) {
	items := m.visibleWorkstreams()
	if len(items) == 0 || m.workstream >= len(items) {
		return workload.Entity{}, false
	}
	return items[m.workstream], true
}
func (m Model) visibleTasks() []workload.Entity {
	workstream, ok := m.selectedWorkstream()
	if !ok {
		return nil
	}
	var result []workload.Entity
	for _, entity := range m.tasks {
		if entity.Project == workstream.Project && entity.Workstream == workstream.ID {
			result = append(result, entity)
		}
	}
	sort.SliceStable(result, func(i, j int) bool {
		if taskStatusRank(result[i].Status) != taskStatusRank(result[j].Status) {
			return taskStatusRank(result[i].Status) < taskStatusRank(result[j].Status)
		}
		if result[i].Order != result[j].Order {
			return result[i].Order < result[j].Order
		}
		return result[i].ID < result[j].ID
	})
	return result
}

func taskStatusRank(status string) int {
	switch status {
	case "coding", "active":
		return 0
	case "self-review", "ready-for-review", "done":
		return 1
	case "in-review":
		return 2
	case "ready":
		return 3
	case "blocked":
		return 4
	case "draft":
		return 5
	case "merged":
		return 6
	case "superseded":
		return 7
	default:
		return 8
	}
}
func (m Model) selectedTask() (workload.Entity, bool) {
	if m.level != tasksLevel {
		return workload.Entity{}, false
	}
	items := m.visibleTasks()
	if len(items) == 0 || m.task >= len(items) {
		return workload.Entity{}, false
	}
	return items[m.task], true
}
func (m Model) selectedEntity() (workload.Entity, bool) {
	switch m.level {
	case projectsLevel:
		return m.selectedProject()
	case workstreamsLevel:
		return m.selectedWorkstream()
	default:
		return m.selectedTask()
	}
}

func (m Model) launchBlocker(task workload.Entity) string {
	if task.Schema != workload.SchemaV1 || task.Legacy {
		return "legacy tasks need canonical schema and metadata"
	}
	if len(task.Diagnostics) > 0 {
		return strings.Join(task.Diagnostics, "; ")
	}
	if task.Status != "ready" {
		return fmt.Sprintf("status is %q, not ready", task.Status)
	}
	if task.Branch == "" {
		return "task has no launch branch"
	}
	if project, ok := m.selectedProject(); ok && len(project.Repositories) > 0 {
		registered := false
		for _, repository := range project.Repositories {
			if repository == task.Repository {
				registered = true
				break
			}
		}
		if !registered {
			return fmt.Sprintf("repository is not registered by project %s", project.ID)
		}
	}
	states := m.taskStates()
	for _, dependency := range task.DependsOn {
		if !workload.DependencySatisfied(states[dependency]) {
			return fmt.Sprintf("dependency %s is %q, not merged", dependency, states[dependency])
		}
	}
	return ""
}

func (m Model) View() string {
	if m.width == 0 {
		return ""
	}
	width := max(60, m.width-4)
	header := m.header(width)
	footerText := "tab live/workloads   enter drill/focus   esc/← back   j/k move   p planner   i source   c check   L launch   r refresh   q quit"
	if m.launching {
		footerText = "launching task…   esc cancel   q quit"
	}
	footer := mutedStyle.Render(footerText)
	body := m.body(width)
	messages := ""
	if m.confirm {
		if task, ok := m.selectedTask(); ok {
			messages += "\n\n" + lipgloss.NewStyle().Bold(true).Foreground(yellow).Render("Launch "+task.ID+"? [y/N]")
		}
	}
	if m.err != nil {
		messages += "\n\n" + errorStyle.Render("! "+m.err.Error())
	}
	if m.runtimeErr != nil {
		messages += "\n\n" + mutedStyle.Render("! "+m.runtimeErr.Error())
	}
	if m.notice != "" {
		messages += "\n\n" + mutedStyle.Render(truncate(m.notice, width))
	}
	return lipgloss.NewStyle().Padding(1, 2).Render(header + "\n\n" + body + messages + "\n\n" + footer)
}

func (m Model) header(width int) string {
	title := logoStyle.Render("◆ WORKLOADS") + "  " + lipgloss.NewStyle().Bold(true).Foreground(ink).Render(m.breadcrumb())
	status := mutedStyle.Render(fmt.Sprintf("%d projects · %d workstreams · %d tasks", len(m.projects), len(m.workstreams), len(m.tasks)))
	if m.loading {
		status = mutedStyle.Render("refreshing · ") + status
	}
	if m.busy {
		status = mutedStyle.Render("working · ") + status
	}
	gap := max(1, width-lipgloss.Width(title)-lipgloss.Width(status))
	return title + strings.Repeat(" ", gap) + status
}

func (m Model) breadcrumb() string {
	parts := []string{"Projects"}
	if project, ok := m.selectedProject(); ok && m.level > projectsLevel {
		parts = append(parts, project.Title)
	}
	if workstream, ok := m.selectedWorkstream(); ok && m.level > workstreamsLevel {
		parts = append(parts, workstream.Title)
	}
	return strings.Join(parts, "  ›  ")
}

func (m Model) body(width int) string {
	leftWidth := min(52, max(30, width*2/5))
	rightWidth := max(24, width-leftWidth-3)
	return lipgloss.JoinHorizontal(lipgloss.Top,
		lipgloss.NewStyle().Width(leftWidth).Render(m.listView(leftWidth)), "   ",
		lipgloss.NewStyle().Width(rightWidth).Render(m.detailView(rightWidth)))
}

func (m Model) listView(width int) string {
	titles := []string{"PROJECTS", "WORKSTREAMS", "TASKS"}
	rows := []string{heading(titles[m.level]), rule(width)}
	items := m.items()
	if len(items) == 0 {
		return strings.Join(append(rows, "", mutedStyle.Render("No workloads at this level.")), "\n")
	}
	available := max(3, m.height-12)
	start := 0
	if m.cursor() >= available {
		start = m.cursor() - available + 1
	}
	end := min(len(items), start+available)
	for index := start; index < end; index++ {
		entity := items[index]
		marker, style := stateMarker(entity.Status)
		label := style.Render(marker) + "  " + truncate(entity.Title, max(8, width-7))
		if index == m.cursor() {
			label = selected(width, label)
		} else {
			label = "  " + label
		}
		rows = append(rows, label)
	}
	return strings.Join(rows, "\n")
}

func (m Model) detailView(width int) string {
	rows := []string{heading("DETAILS"), rule(width)}
	entity, ok := m.selectedEntity()
	if !ok {
		return strings.Join(rows, "\n")
	}
	marker, style := stateMarker(entity.Status)
	rows = append(rows, "", style.Bold(true).Render(marker+"  "+strings.ToUpper(entity.Status)), "", lipgloss.NewStyle().Bold(true).Foreground(ink).Render(truncate(entity.Title, width)))
	rows = append(rows, "", field("ID", entity.ID, width), field("TYPE", entity.Type, width))
	repository := entity.Repository
	if entity.Type == "project" {
		repository = entity.RootRepository
	}
	if repository != "" {
		rows = append(rows, field("REPO", homePath(repository), width))
	}
	if entity.Branch != "" {
		rows = append(rows, field("BRANCH", entity.Branch, width))
	}
	if entity.Schema != "" {
		rows = append(rows, field("SCHEMA", entity.Schema, width))
	}
	if entity.Legacy {
		rows = append(rows, errorStyle.Render("LEGACY  migration required before launch"))
	}
	if len(entity.DependsOn) > 0 {
		rows = append(rows, "", mutedStyle.Bold(true).Render("DEPENDENCIES"))
		states := m.taskStates()
		for _, dependency := range entity.DependsOn {
			marker, depStyle := stateMarker(states[dependency])
			rows = append(rows, "  "+depStyle.Render(marker)+" "+truncate(dependency+" ["+states[dependency]+"]", max(8, width-4)))
		}
	}
	if entity.Type == "task" {
		if session, ok := m.liveSession(entity); ok {
			rows = append(rows, "", field("RUNTIME", string(session.State), width), field("SESSION", session.Name, width))
		} else {
			rows = append(rows, "", field("RUNTIME", "no matching tmux workspace", width))
		}
	}
	if len(entity.Diagnostics) > 0 {
		rows = append(rows, "", errorStyle.Bold(true).Render("DIAGNOSTICS"))
		for _, diagnostic := range entity.Diagnostics {
			rows = append(rows, errorStyle.Render("! "+truncate(diagnostic, max(8, width-2))))
		}
	}
	if entity.Type == "task" && (m.launching || len(m.launchProgress) > 0) {
		rows = append(rows, "", mutedStyle.Bold(true).Render("LAUNCH PROGRESS"))
		start := max(0, len(m.launchProgress)-6)
		for _, progress := range m.launchProgress[start:] {
			marker := "·"
			progressStyle := mutedStyle
			if progress.State == tasklaunch.EventCompleted {
				marker, progressStyle = "✓", lipgloss.NewStyle().Foreground(green)
			} else if progress.State == tasklaunch.EventFailed {
				marker, progressStyle = "!", errorStyle
			}
			rows = append(rows, "  "+progressStyle.Render(marker)+" "+truncate(string(progress.Stage)+" · "+progress.Message, max(8, width-4)))
		}
	}
	rows = append(rows, "", mutedStyle.Render(truncate(entity.SourcePath, width)))
	return strings.Join(rows, "\n")
}

func (m Model) liveSession(task workload.Entity) (runtimeworkbench.Session, bool) {
	for _, session := range m.snapshot.Sessions {
		if session.TaskID == task.ID {
			return session, true
		}
		for _, window := range session.Windows {
			matchesFX := task.FXSessionID != "" && window.FXSessionID == task.FXSessionID
			if !matchesFX {
				for _, pane := range window.Panes {
					if task.FXSessionID != "" && pane.FXSessionID == task.FXSessionID {
						matchesFX = true
						break
					}
				}
			}
			if window.TaskID == task.ID || matchesFX {
				target := session
				target.TaskID, target.Repository, target.Worktree = window.TaskID, window.Repository, window.Worktree
				target.Windows = []runtimeworkbench.Window{window}
				return target, true
			}
		}
	}
	for _, session := range m.snapshot.Sessions {
		worktree := session.Worktree
		if worktree == "" {
			worktree = session.Path
		}
		if task.Branch == "" || filepath.Base(worktree) != task.Branch {
			continue
		}
		repository := filepath.Base(filepath.Clean(session.Repository))
		if repository == "." || repository == string(filepath.Separator) {
			repository = ""
		}
		if repository == "" || repository == filepath.Base(filepath.Clean(task.Repository)) {
			return session, true
		}
	}
	return runtimeworkbench.Session{}, false
}
func (m Model) taskStates() map[string]string {
	states := make(map[string]string, len(m.tasks))
	project, _ := m.selectedProject()
	for _, task := range m.tasks {
		if project.ID == "" || task.Project == project.ID {
			states[task.ID] = task.Status
		}
	}
	return states
}
func stateMarker(state string) (string, lipgloss.Style) {
	switch state {
	case "coding", "active":
		return "◆", lipgloss.NewStyle().Foreground(yellow)
	case "self-review", "ready-for-review", "done":
		return "●", lipgloss.NewStyle().Foreground(cyan)
	case "in-review":
		return "◉", lipgloss.NewStyle().Foreground(purple)
	case "ready":
		return "◇", lipgloss.NewStyle().Foreground(cyan)
	case "blocked":
		return "!", lipgloss.NewStyle().Foreground(coral)
	case "merged":
		return "✓", lipgloss.NewStyle().Foreground(green)
	case "superseded":
		return "×", lipgloss.NewStyle().Foreground(muted)
	default:
		return "○", lipgloss.NewStyle().Foreground(muted)
	}
}
func heading(value string) string {
	return lipgloss.NewStyle().Bold(true).Foreground(ink).Render(value)
}
func rule(width int) string {
	return lipgloss.NewStyle().Foreground(panel).Render(strings.Repeat("━", width))
}
func selected(width int, value string) string {
	return lipgloss.NewStyle().Width(width).Background(selection).Foreground(ink).Bold(true).Render("  " + value)
}
func field(label, value string, width int) string {
	return mutedStyle.Bold(true).Render(label) + "  " + truncate(value, max(8, width-len(label)-2))
}
func truncate(value string, width int) string {
	if width <= 0 || lipgloss.Width(value) <= width {
		return value
	}
	if width == 1 {
		return "…"
	}
	runes := []rune(value)
	for len(runes) > 0 && lipgloss.Width(string(runes)+"…") > width {
		runes = runes[:len(runes)-1]
	}
	return string(runes) + "…"
}
func homePath(path string) string {
	home, err := os.UserHomeDir()
	if err == nil && (path == home || strings.HasPrefix(path, home+string(os.PathSeparator))) {
		return "~" + strings.TrimPrefix(path, home)
	}
	return filepath.Clean(path)
}
