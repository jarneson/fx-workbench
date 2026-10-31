package taskui

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	runtimeworkbench "github.com/jarneson/fx-workbench/internal/workbench"
	"github.com/jarneson/fx-workbench/internal/workload"
)

const (
	defaultSidebarRefreshInterval = 2 * time.Second
	sidebarTaskActionTimeout      = 90 * time.Second
	sidebarTaskTitleMaxWidth      = 20
)

type sidebarLoadMsg struct {
	tasks           []workload.Entity
	workstreamTitle string
	snapshot        runtimeworkbench.Snapshot
	catalogErr      error
	runtimeErr      error
	catalogOK       bool
	runtimeOK       bool
}

type sidebarTickMsg struct{}

type sidebarActionMsg struct {
	taskID string
	action string
	err    error
}

type SidebarActions interface {
	Focus(context.Context, workload.Entity, runtimeworkbench.Session) error
	Resume(context.Context, workload.Entity) error
	Review(context.Context, workload.Entity, runtimeworkbench.Session) error
	OpenPullRequest(context.Context, workload.Entity, bool) error
}

// SidebarModel is a compact, read-only view of one workstream's tasks.
type SidebarModel struct {
	catalog Catalog
	runtime Runtime
	actions SidebarActions

	project         string
	workstream      string
	workstreamTitle string
	interval        time.Duration

	tasks    []workload.Entity
	snapshot runtimeworkbench.Snapshot
	selected int
	width    int
	height   int
	loading  bool
	focusing bool

	catalogErr error
	runtimeErr error
	notice     string
}

func NewSidebarModel(catalog Catalog, runtime Runtime, project, workstream string, interval time.Duration) SidebarModel {
	if interval <= 0 {
		interval = defaultSidebarRefreshInterval
	}
	return SidebarModel{
		catalog: catalog, runtime: runtime, project: project, workstream: workstream,
		interval: interval, loading: true,
	}
}

func (m SidebarModel) WithActions(actions SidebarActions) SidebarModel {
	m.actions = actions
	return m
}

func (m SidebarModel) Init() tea.Cmd { return m.load() }

func (m SidebarModel) load() tea.Cmd {
	return func() tea.Msg {
		msg := sidebarLoadMsg{}
		entities, err := m.catalog.List(workload.ListOptions{
			Project: m.project, Workstream: m.workstream, IncludeClosed: true,
		})
		if err != nil {
			msg.catalogErr = fmt.Errorf("catalog refresh failed: %w", err)
		} else {
			msg.catalogOK = true
			for _, entity := range entities {
				switch {
				case entity.Type == "workstream" && entity.ID == m.workstream:
					msg.workstreamTitle = entity.Title
				case entity.Type == "task" && entity.Project == m.project && entity.Workstream == m.workstream:
					msg.tasks = append(msg.tasks, entity)
				}
			}
			sortSidebarTasks(msg.tasks)
		}

		if m.runtime == nil {
			msg.runtimeOK = true
			return msg
		}
		ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
		defer cancel()
		msg.snapshot, err = m.runtime.Snapshot(ctx)
		if err != nil {
			msg.runtimeErr = fmt.Errorf("runtime refresh failed: %w", err)
		} else {
			msg.runtimeOK = true
		}
		return msg
	}
}

func (m SidebarModel) scheduleRefresh() tea.Cmd {
	return tea.Tick(m.interval, func(time.Time) tea.Msg { return sidebarTickMsg{} })
}

func (m SidebarModel) runTaskAction(action string, task workload.Entity, target runtimeworkbench.Session, restoreAgent bool) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), sidebarTaskActionTimeout)
		defer cancel()
		var err error
		switch action {
		case "focus":
			if m.actions != nil {
				err = m.actions.Focus(ctx, task, target)
			} else if m.runtime != nil {
				err = m.runtime.Focus(ctx, target)
			}
		case "resume":
			if m.actions == nil {
				err = fmt.Errorf("resume action is unavailable")
			} else {
				err = m.actions.Resume(ctx, task)
			}
		case "review":
			if m.actions == nil {
				err = fmt.Errorf("review action is unavailable")
			} else {
				err = m.actions.Review(ctx, task, target)
			}
		case "pull-request":
			if m.actions == nil {
				err = fmt.Errorf("pull request action is unavailable")
			} else {
				err = m.actions.OpenPullRequest(ctx, task, restoreAgent)
			}
		}
		return sidebarActionMsg{taskID: task.ID, action: action, err: err}
	}
}

func (m SidebarModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := message.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case sidebarLoadMsg:
		selectedID := m.selectedID()
		m.loading = false
		m.catalogErr, m.runtimeErr = msg.catalogErr, msg.runtimeErr
		if msg.catalogOK {
			m.tasks = msg.tasks
			if msg.workstreamTitle != "" {
				m.workstreamTitle = msg.workstreamTitle
			}
			m.restoreSelection(selectedID)
		}
		if msg.runtimeOK {
			m.snapshot = msg.snapshot
		}
		return m, m.scheduleRefresh()
	case sidebarTickMsg:
		if !m.loading && !m.focusing {
			m.loading = true
			return m, m.load()
		}
		return m, m.scheduleRefresh()
	case sidebarActionMsg:
		m.focusing = false
		if msg.err != nil {
			m.notice = msg.action + " failed for " + msg.taskID + ": " + msg.err.Error()
			return m, nil
		}
		m.notice = ""
		return m, nil
	case tea.KeyMsg:
		if m.focusing {
			if msg.String() == "q" || msg.String() == "ctrl+c" {
				return m, tea.Quit
			}
			return m, nil
		}
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			return m, tea.Quit
		case "j", "down":
			m.move(1)
		case "k", "up":
			m.move(-1)
		case "g", "home":
			m.selected = m.firstSelectable()
		case "G", "end":
			m.selected = m.lastSelectable()
		case "enter":
			if task, ok := m.selectedTask(); ok {
				status := sidebarStatusSection(task.Status)
				target, live := matchTaskSession(m.snapshot, task)
				live = live && taskAgentReady(target)
				switch status {
				case "coding":
					m.focusing = true
					if !live {
						m.notice = "resuming " + task.ID + "…"
						return m, m.runTaskAction("resume", task, runtimeworkbench.Session{}, false)
					}
					m.notice = "focusing " + task.ID + "…"
					return m, m.runTaskAction("focus", task, target, false)
				case "self-review":
					m.focusing = true
					m.notice = "opening review for " + task.ID + "…"
					return m, m.runTaskAction("review", task, target, false)
				case "in-review":
					m.focusing = true
					m.notice = "opening pull request for " + task.ID + "…"
					return m, m.runTaskAction("pull-request", task, runtimeworkbench.Session{}, !live)
				default:
					m.notice = "no action for " + task.ID + " in " + status
				}
			}
		}
	}
	return m, nil
}

func sortSidebarTasks(tasks []workload.Entity) {
	sort.SliceStable(tasks, func(i, j int) bool {
		if left, right := sidebarStatusRank(tasks[i].Status), sidebarStatusRank(tasks[j].Status); left != right {
			return left < right
		}
		if tasks[i].Order != tasks[j].Order {
			if tasks[i].Order == 0 {
				return false
			}
			if tasks[j].Order == 0 {
				return true
			}
			return tasks[i].Order < tasks[j].Order
		}
		return tasks[i].ID < tasks[j].ID
	})
}

func sidebarStatusRank(status string) int {
	switch sidebarStatusSection(status) {
	case "coding":
		return 0
	case "self-review":
		return 1
	case "in-review":
		return 2
	case "ready":
		return 3
	case "blocked":
		return 4
	case "merged":
		return 5
	case "draft":
		return 6
	case "superseded":
		return 7
	default:
		return 8
	}
}

func sidebarStatusSection(status string) string {
	switch status {
	case "active":
		return "coding"
	case "done", "ready-for-review":
		return "self-review"
	default:
		return status
	}
}

func (m SidebarModel) selectedID() string {
	if task, ok := m.selectedTask(); ok {
		return task.ID
	}
	return ""
}

func (m *SidebarModel) restoreSelection(id string) {
	if id != "" {
		for index, task := range m.tasks {
			if task.ID == id && sidebarTaskSelectable(task) {
				m.selected = index
				return
			}
		}
	}
	if m.selected >= 0 && m.selected < len(m.tasks) && sidebarTaskSelectable(m.tasks[m.selected]) {
		return
	}
	m.selected = m.firstSelectable()
}

func (m *SidebarModel) move(delta int) {
	if delta == 0 {
		return
	}
	if m.selected < 0 {
		if delta > 0 {
			m.selected = m.firstSelectable()
		} else {
			m.selected = m.lastSelectable()
		}
		return
	}
	for index := m.selected + delta; index >= 0 && index < len(m.tasks); index += delta {
		if sidebarTaskSelectable(m.tasks[index]) {
			m.selected = index
			return
		}
	}
}

func (m SidebarModel) firstSelectable() int {
	for index, task := range m.tasks {
		if sidebarTaskSelectable(task) {
			return index
		}
	}
	return -1
}

func (m SidebarModel) lastSelectable() int {
	for index := len(m.tasks) - 1; index >= 0; index-- {
		if sidebarTaskSelectable(m.tasks[index]) {
			return index
		}
	}
	return -1
}

func sidebarTaskSelectable(task workload.Entity) bool { return task.Status != "merged" }

func (m SidebarModel) selectedTask() (workload.Entity, bool) {
	if len(m.tasks) == 0 || m.selected < 0 || m.selected >= len(m.tasks) || !sidebarTaskSelectable(m.tasks[m.selected]) {
		return workload.Entity{}, false
	}
	return m.tasks[m.selected], true
}

func (m SidebarModel) View() string {
	if m.width <= 0 {
		return ""
	}
	width := max(1, m.width)
	projectHeader := logoStyle.Render(truncate(strings.ToUpper(m.project), width))
	workstreamTitle := m.workstreamTitle
	if workstreamTitle == "" {
		workstreamTitle = m.workstream
	}
	workstreamHeader := lipgloss.NewStyle().Bold(true).Foreground(ink).Render(truncate(titleCase(workstreamTitle), width))
	if remaining := width - lipgloss.Width(workstreamHeader) - 1; m.loading && remaining > 0 {
		workstreamHeader += " " + mutedStyle.Render(truncate("refreshing", remaining))
	}
	rows := []string{projectHeader, workstreamHeader, rule(width)}
	listRows, selectedRow := m.taskListRows(width)
	if len(m.tasks) == 0 {
		rows = append(rows, listRows...)
		rows = append(rows, mutedStyle.Render(truncate("No tasks in this workstream.", width)))
	} else {
		visible := max(1, m.height-6)
		start := 0
		if selectedRow >= visible {
			start = selectedRow - visible + 1
		}
		end := min(len(listRows), start+visible)
		rows = append(rows, listRows[start:end]...)
	}
	if m.catalogErr != nil {
		rows = append(rows, errorStyle.Render(truncate("! "+m.catalogErr.Error(), width)))
	}
	if m.runtimeErr != nil {
		rows = append(rows, errorStyle.Render(truncate("! "+m.runtimeErr.Error(), width)))
	}
	if m.notice != "" {
		rows = append(rows, mutedStyle.Render(truncate(m.notice, width)))
	}
	rows = append(rows, mutedStyle.Render(truncate("j/k move · enter focus · q quit", width)))
	return strings.Join(rows, "\n")
}

func titleCase(value string) string {
	words := strings.Fields(value)
	for index, word := range words {
		runes := []rune(word)
		if len(runes) > 0 {
			runes[0] = []rune(strings.ToUpper(string(runes[0])))[0]
			words[index] = string(runes)
		}
	}
	return strings.Join(words, " ")
}

func sidebarStatusLabel(status string) string {
	return strings.ToUpper(strings.ReplaceAll(status, "-", " "))
}

func (m SidebarModel) taskListRows(width int) ([]string, int) {
	rows := make([]string, 0, len(m.tasks)+4)
	selectedRow := 0
	primary := []string{"coding", "self-review", "in-review", "ready", "blocked", "merged"}
	primarySet := make(map[string]bool, len(primary))
	appendSection := func(section string) {
		primarySet[section] = true
		_, style := stateMarker(section)
		rows = append(rows, style.Bold(true).Render(truncate(sidebarStatusLabel(section), width)))
		for index, task := range m.tasks {
			if sidebarStatusSection(task.Status) != section {
				continue
			}
			if index == m.selected {
				selectedRow = len(rows)
			}
			rows = append(rows, m.taskRow(task, index == m.selected, width))
		}
		rows = append(rows, "")
	}
	for _, section := range primary {
		appendSection(section)
	}

	section := ""
	for index, task := range m.tasks {
		status := sidebarStatusSection(task.Status)
		if primarySet[status] {
			continue
		}
		if status != section {
			if section != "" {
				rows = append(rows, "")
			}
			section = status
			_, style := stateMarker(section)
			rows = append(rows, style.Bold(true).Render(truncate(sidebarStatusLabel(section), width)))
		}
		if index == m.selected {
			selectedRow = len(rows)
		}
		rows = append(rows, m.taskRow(task, index == m.selected, width))
	}
	if section != "" {
		rows = append(rows, "")
	}
	return rows, selectedRow
}

func (m SidebarModel) taskRow(task workload.Entity, active bool, width int) string {
	runtimeState := "offline"
	if target, ok := matchTaskSession(m.snapshot, task); ok {
		runtimeState = sidebarRuntimeState(target)
	}
	runtimeMarker, runtimeStyle := sidebarRuntimeMarker(runtimeState)
	prefix := "  "
	if active {
		prefix = "> "
	}
	order := "--"
	if task.Order > 0 {
		order = fmt.Sprintf("%02d", task.Order)
	}
	markerWidth := lipgloss.Width(prefix) + 2
	row := truncate(prefix+runtimeMarker, width)
	if width > markerWidth {
		title := truncate(task.Title, sidebarTaskTitleMaxWidth)
		label := truncate(order+" "+title, width-markerWidth)
		row = prefix + runtimeStyle.Render(runtimeMarker) + " " + label
	}
	if active {
		return lipgloss.NewStyle().Width(width).Background(selection).Foreground(ink).Bold(true).Render(row)
	}
	return row
}

func sidebarRuntimeMarker(state string) (string, lipgloss.Style) {
	switch state {
	case "attention":
		return "!", lipgloss.NewStyle().Foreground(coral).Bold(true)
	case "running":
		return "●", lipgloss.NewStyle().Foreground(green)
	case "idle":
		return "○", lipgloss.NewStyle().Foreground(yellow)
	default:
		return "×", mutedStyle
	}
}

func sidebarRuntimeState(session runtimeworkbench.Session) string {
	state := session.State
	for _, window := range session.Windows {
		for _, pane := range window.Panes {
			candidate := pane.AgentState
			if pane.NeedsAttention {
				candidate = runtimeworkbench.StateAttention
			}
			if sidebarRuntimeRank(candidate) < sidebarRuntimeRank(state) {
				state = candidate
			}
		}
	}
	switch state {
	case runtimeworkbench.StateAttention:
		return "attention"
	case runtimeworkbench.StateRunning:
		return "running"
	default:
		return "idle"
	}
}

func sidebarRuntimeRank(state runtimeworkbench.AgentState) int {
	switch state {
	case runtimeworkbench.StateAttention:
		return 0
	case runtimeworkbench.StateRunning:
		return 1
	case runtimeworkbench.StateIdle:
		return 2
	default:
		return 3
	}
}

// matchTaskSession applies correlation in decreasing confidence: task/window tags,
// durable fx session identity, then the conservative legacy worktree identity.
// The returned session contains only the matched task panes when a pane-level
// identity was available, allowing Focus to select the exact worker pane.
func matchTaskSession(snapshot runtimeworkbench.Snapshot, task workload.Entity) (runtimeworkbench.Session, bool) {
	for _, session := range snapshot.Sessions {
		if target, ok := matchingPanes(session, func(pane runtimeworkbench.Pane) bool { return pane.TaskID == task.ID }); ok {
			return target, true
		}
	}
	for _, session := range snapshot.Sessions {
		if session.TaskID == task.ID {
			return session, true
		}
	}
	if task.FXSessionID != "" {
		for _, session := range snapshot.Sessions {
			if target, ok := matchingPanes(session, func(pane runtimeworkbench.Pane) bool { return pane.FXSessionID == task.FXSessionID }); ok {
				return target, true
			}
		}
	}
	for _, session := range snapshot.Sessions {
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

func taskAgentReady(session runtimeworkbench.Session) bool {
	for _, window := range session.Windows {
		for _, pane := range window.Panes {
			if pane.TaskID != "" && pane.FXSessionID == "" {
				return false
			}
		}
	}
	return true
}

func matchingPanes(session runtimeworkbench.Session, matches func(runtimeworkbench.Pane) bool) (runtimeworkbench.Session, bool) {
	target := session
	target.Windows = nil
	for _, window := range session.Windows {
		matchedWindow := window
		matchedWindow.Panes = nil
		for _, pane := range window.Panes {
			if matches(pane) {
				// A pane-level identity is sufficient to focus a live worker even
				// before fx has published its first lifecycle state or title.
				if pane.AgentState == runtimeworkbench.StateUnknown && pane.AgentTitle == "" {
					pane.AgentState = runtimeworkbench.StateIdle
				}
				matchedWindow.Panes = append(matchedWindow.Panes, pane)
			}
		}
		if len(matchedWindow.Panes) > 0 {
			target.Windows = append(target.Windows, matchedWindow)
		}
	}
	if len(target.Windows) == 0 {
		return runtimeworkbench.Session{}, false
	}
	target.State = runtimeworkbench.StateUnknown
	return target, true
}
