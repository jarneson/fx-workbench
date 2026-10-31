package cockpit

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/jarneson/fx-workbench/internal/taskui"
	"github.com/jarneson/fx-workbench/internal/workbench"
)

type Model struct {
	live      workbench.Model
	workloads taskui.Model
	mode      int
}

func NewModel(live workbench.Model, workloads taskui.Model) Model {
	return Model{live: live, workloads: workloads}
}

func (m Model) Init() tea.Cmd { return tea.Batch(m.live.Init(), m.workloads.Init()) }

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "tab":
			m.mode = (m.mode + 1) % 2
			return m, nil
		case "1":
			m.mode = 0
			return m, nil
		case "2":
			m.mode = 1
			return m, nil
		}
		if m.mode == 0 {
			updated, command := m.live.Update(msg)
			m.live = updated.(workbench.Model)
			return m, command
		}
		updated, command := m.workloads.Update(msg)
		m.workloads = updated.(taskui.Model)
		return m, command
	}

	live, liveCommand := m.live.Update(msg)
	workloads, workloadsCommand := m.workloads.Update(msg)
	m.live = live.(workbench.Model)
	m.workloads = workloads.(taskui.Model)
	return m, tea.Batch(liveCommand, workloadsCommand)
}

func (m Model) View() string {
	if m.mode == 0 {
		return m.live.View()
	}
	return m.workloads.View()
}
