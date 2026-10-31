package workbench

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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

	logoStyle   = lipgloss.NewStyle().Bold(true).Foreground(purple)
	tagStyle    = lipgloss.NewStyle().Bold(true).Foreground(cyan)
	mutedStyle  = lipgloss.NewStyle().Foreground(muted)
	errorStyle  = lipgloss.NewStyle().Foreground(coral)
	footerStyle = lipgloss.NewStyle().Foreground(muted)
)

type snapshotMsg struct {
	snapshot Snapshot
	err      error
}

type focusMsg struct{ err error }
type discoveryMsg struct {
	generation int
	workspaces []Workspace
	err        error
}
type openMsg struct {
	result OpenResult
	err    error
}

type Model struct {
	source              Source
	discovery           WorkspaceDiscovery
	launcher            WorkspaceLauncher
	snapshot            Snapshot
	cursor              int
	width               int
	height              int
	loading             bool
	err                 error
	palette             bool
	query               string
	workspaces          []Workspace
	launchCursor        int
	discoveryGeneration int
	discovering         bool
	launching           bool
}

func NewModel(source Source, discovery WorkspaceDiscovery, launcher WorkspaceLauncher) Model {
	return Model{source: source, discovery: discovery, launcher: launcher, loading: true}
}

func (m Model) Init() tea.Cmd {
	return m.load()
}

func (m Model) load() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
		defer cancel()
		snapshot, err := m.source.Snapshot(ctx)
		return snapshotMsg{snapshot: snapshot, err: err}
	}
}

func (m Model) focus(session Session) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
		defer cancel()
		return focusMsg{err: m.source.Focus(ctx, session)}
	}
}

func (m Model) discover() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		workspaces, err := m.discovery.Discover(ctx)
		return discoveryMsg{generation: m.discoveryGeneration, workspaces: workspaces, err: err}
	}
}

func (m Model) open(workspace Workspace) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		result, err := m.launcher.OpenWorkspace(ctx, workspace)
		return openMsg{result: result, err: err}
	}
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case snapshotMsg:
		m.loading = false
		m.err = msg.err
		if msg.err == nil {
			m.snapshot = msg.snapshot
			if m.cursor >= len(m.snapshot.Sessions) {
				m.cursor = max(0, len(m.snapshot.Sessions)-1)
			}
		}
	case discoveryMsg:
		if !m.palette || msg.generation != m.discoveryGeneration {
			break
		}
		m.discovering = false
		m.err = msg.err
		m.workspaces = nil
		if msg.err == nil {
			m.workspaces = msg.workspaces
			m.launchCursor = 0
		}
	case focusMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		return m, tea.Quit
	case openMsg:
		m.launching = false
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		return m, tea.Quit
	case tea.KeyMsg:
		if m.palette {
			return m.updatePalette(msg)
		}
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			return m, tea.Quit
		case "j", "down":
			if m.cursor < len(m.snapshot.Sessions)-1 {
				m.cursor++
			}
		case "k", "up":
			if m.cursor > 0 {
				m.cursor--
			}
		case "g", "home":
			m.cursor = 0
		case "G", "end":
			m.cursor = max(0, len(m.snapshot.Sessions)-1)
		case "r":
			m.loading = true
			return m, m.load()
		case "o":
			if m.discovery != nil && m.launcher != nil {
				m.palette, m.discovering, m.err = true, true, nil
				m.query, m.launchCursor, m.workspaces = "", 0, nil
				m.discoveryGeneration++
				return m, m.discover()
			}
		case "enter":
			if len(m.snapshot.Sessions) > 0 {
				m.err = nil
				return m, m.focus(m.snapshot.Sessions[m.cursor])
			}
		}
	}
	return m, nil
}

func (m Model) updatePalette(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.launching {
		return m, nil
	}
	workspaces := m.filteredWorkspaces()
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		m.palette, m.err = false, nil
		return m, nil
	case "up":
		if m.launchCursor > 0 {
			m.launchCursor--
		}
	case "down":
		if m.launchCursor < len(workspaces)-1 {
			m.launchCursor++
		}
	case "backspace":
		runes := []rune(m.query)
		if len(runes) > 0 {
			m.query = string(runes[:len(runes)-1])
			m.launchCursor = 0
		}
	case "enter":
		if !m.discovering && len(workspaces) > 0 {
			m.launching, m.err = true, nil
			return m, m.open(workspaces[m.launchCursor])
		}
	default:
		if msg.Type == tea.KeyRunes {
			m.query += string(msg.Runes)
			m.launchCursor = 0
		}
	}
	return m, nil
}

func (m Model) filteredWorkspaces() []Workspace {
	query := strings.ToLower(strings.TrimSpace(m.query))
	if query == "" {
		return m.workspaces
	}
	var filtered []Workspace
	for _, workspace := range m.workspaces {
		haystack := strings.ToLower(workspace.Label + " " + workspace.Path + " " + string(workspace.Kind))
		if strings.Contains(haystack, query) {
			filtered = append(filtered, workspace)
		}
	}
	return filtered
}

func (m Model) View() string {
	if m.width == 0 {
		return ""
	}
	bodyWidth := max(40, m.width-4)
	leftWidth := min(48, max(30, bodyWidth*2/5))
	rightWidth := max(20, bodyWidth-leftWidth-3)

	header := m.header(bodyWidth)
	if m.palette {
		body := m.launchPalette(bodyWidth)
		footerText := "type to search   ↑/↓ move   enter open   esc close"
		if m.launching {
			footerText = "opening workspace…"
		}
		footer := footerStyle.Render(footerText)
		if m.err != nil {
			footer = errorStyle.Render("! "+m.err.Error()) + "  " + footer
		}
		return lipgloss.NewStyle().Padding(1, 2).Render(header + "\n\n" + body + "\n\n" + footer)
	}
	left := lipgloss.NewStyle().Width(leftWidth).Render(m.sessionList(leftWidth))
	right := lipgloss.NewStyle().Width(rightWidth).Render(m.details(rightWidth))
	body := lipgloss.JoinHorizontal(lipgloss.Top, left, "   ", right)
	footer := footerStyle.Render("tab live/workloads   ↑/k ↓/j move   enter focus   o open workspace   r refresh   q quit")
	if m.err != nil {
		footer = errorStyle.Render("! "+m.err.Error()) + "  " + footer
	}

	return lipgloss.NewStyle().Padding(1, 2).Render(header + "\n\n" + body + "\n\n" + footer)
}

func (m Model) header(width int) string {
	title := logoStyle.Render("◆ FX WORKBENCH")
	counts := []string{
		fmt.Sprintf("%d workspaces", len(m.snapshot.Sessions)),
		fmt.Sprintf("%d active", countState(m.snapshot.Sessions, StateRunning)),
		fmt.Sprintf("%d attention", countState(m.snapshot.Sessions, StateAttention)),
	}
	status := mutedStyle.Render(strings.Join(counts, "  ·  "))
	if m.loading {
		status = mutedStyle.Render("refreshing  ·  ") + status
	}
	gap := max(1, width-lipgloss.Width(title)-lipgloss.Width(status))
	return title + strings.Repeat(" ", gap) + status
}

func (m Model) sessionList(width int) string {
	heading := lipgloss.NewStyle().Bold(true).Foreground(ink).Render("WORKSPACES")
	rule := lipgloss.NewStyle().Foreground(panel).Render(strings.Repeat("━", width))
	if len(m.snapshot.Sessions) == 0 {
		return heading + "\n" + rule + "\n\n" + mutedStyle.Render("No tmux sessions found.")
	}

	rows := []string{heading, rule}
	available := max(3, m.height-9)
	start := 0
	if m.cursor >= available {
		start = m.cursor - available + 1
	}
	end := min(len(m.snapshot.Sessions), start+available)
	for i := start; i < end; i++ {
		session := m.snapshot.Sessions[i]
		marker, markerStyle := stateMarker(session.State)
		nameWidth := max(8, width-12)
		name := truncate(session.Name, nameWidth)
		row := fmt.Sprintf("%s  %-*s %2d", markerStyle.Render(marker), nameWidth, name, session.Agents)
		if session.Attached {
			row += " " + tagStyle.Render("●")
		}
		if i == m.cursor {
			row = lipgloss.NewStyle().Width(width).Background(selection).Foreground(ink).Bold(true).Render("  " + row)
		} else {
			row = "  " + row
		}
		rows = append(rows, row)
	}
	return strings.Join(rows, "\n")
}

func (m Model) launchPalette(width int) string {
	header := lipgloss.NewStyle().Bold(true).Foreground(ink).Render("OPEN WORKSPACE")
	rule := lipgloss.NewStyle().Foreground(panel).Render(strings.Repeat("━", width))
	query := tagStyle.Render("> ") + m.query + "█"
	rows := []string{header, rule, "", query, ""}
	if m.discovering {
		return strings.Join(append(rows, mutedStyle.Render("Discovering repositories and worktrees…")), "\n")
	}
	workspaces := m.filteredWorkspaces()
	if len(workspaces) == 0 {
		return strings.Join(append(rows, mutedStyle.Render("No matching workspaces.")), "\n")
	}
	available := max(3, m.height-11)
	start := 0
	if m.launchCursor >= available {
		start = m.launchCursor - available + 1
	}
	end := min(len(workspaces), start+available)
	for index := start; index < end; index++ {
		workspace := workspaces[index]
		kind := "REPO"
		style := tagStyle
		if workspace.Kind == WorkspaceWorktree {
			kind = "TREE"
			style = lipgloss.NewStyle().Bold(true).Foreground(green)
		}
		labelWidth := max(8, width-12)
		row := fmt.Sprintf("%-4s  %s", style.Render(kind), truncate(workspace.Label, labelWidth))
		if index == m.launchCursor {
			row = lipgloss.NewStyle().Width(width).Background(selection).Foreground(ink).Bold(true).Render("  " + row)
		} else {
			row = "  " + row
		}
		rows = append(rows, row)
	}
	if m.launching {
		rows = append(rows, "", mutedStyle.Render("Opening workspace…"))
	}
	return strings.Join(rows, "\n")
}

func (m Model) details(width int) string {
	heading := lipgloss.NewStyle().Bold(true).Foreground(ink).Render("WORKSPACE")
	rule := lipgloss.NewStyle().Foreground(panel).Render(strings.Repeat("━", width))
	if len(m.snapshot.Sessions) == 0 {
		return heading + "\n" + rule + "\n\n" + mutedStyle.Render("Start a session, then press r.")
	}
	session := m.snapshot.Sessions[m.cursor]
	marker, markerStyle := stateMarker(session.State)
	role := session.Role
	if !session.Explicit {
		role += " · inferred"
	}
	lines := []string{
		heading,
		rule,
		"",
		markerStyle.Bold(true).Render(marker+"  ") + lipgloss.NewStyle().Bold(true).Foreground(ink).Render(session.Name),
		mutedStyle.Render(strings.ToUpper(role)),
		"",
		label("PATH") + "  " + truncate(homePath(session.Path), max(12, width-8)),
		label("AGE") + "   " + formatAge(session.CreatedAt),
		label("MUX") + fmt.Sprintf("   %d windows · %d agents", len(session.Windows), session.Agents),
	}
	if session.WorkspaceID != "" {
		lines = append(lines, label("WORKSPACE")+"  "+session.WorkspaceID)
	}
	if session.Repository != "" {
		lines = append(lines, label("REPOSITORY")+"  "+truncate(homePath(session.Repository), max(12, width-14)))
	}
	if session.Worktree != "" && session.Worktree != session.Repository {
		lines = append(lines, label("WORKTREE")+"  "+truncate(homePath(session.Worktree), max(12, width-12)))
	}
	if session.TaskID != "" {
		lines = append(lines, label("TASK")+"  "+truncate(session.TaskID, max(12, width-8)))
	}
	if session.Parent != "" {
		parent := session.Parent
		if session.ParentName != "" {
			parent = session.ParentName + " (" + session.Parent + ")"
		}
		lines = append(lines, label("ROOT")+"  "+parent)
	}
	lines = append(lines, "", label("WINDOWS"))
	for _, window := range session.Windows {
		prefix := "  "
		if window.Active {
			prefix = tagStyle.Render("› ")
		}
		title := fmt.Sprintf("%s%s:%d  %s", prefix, window.Index, len(window.Panes), window.Name)
		lines = append(lines, truncate(title, width))
		for _, pane := range window.Panes {
			if pane.AgentTitle == "" {
				continue
			}
			pm, ps := stateMarker(pane.AgentState)
			lines = append(lines, "    "+ps.Render(pm)+" "+mutedStyle.Render(truncate(pane.AgentTitle, max(8, width-8))))
		}
	}
	return strings.Join(lines, "\n")
}

func stateMarker(state AgentState) (string, lipgloss.Style) {
	switch state {
	case StateAttention:
		return "!", lipgloss.NewStyle().Foreground(coral)
	case StateRunning:
		return "◆", lipgloss.NewStyle().Foreground(yellow)
	case StateIdle:
		return "●", lipgloss.NewStyle().Foreground(green)
	default:
		return "○", lipgloss.NewStyle().Foreground(muted)
	}
}

func countState(sessions []Session, state AgentState) int {
	count := 0
	for _, session := range sessions {
		if session.State == state {
			count++
		}
	}
	return count
}

func label(value string) string { return mutedStyle.Bold(true).Render(value) }

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
	return path
}

func formatAge(created time.Time) string {
	if created.IsZero() || created.Unix() <= 0 {
		return "unknown"
	}
	d := time.Since(created)
	if d < time.Hour {
		return fmt.Sprintf("%dm", max(0, int(d.Minutes())))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}
