package cockpit

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/jarneson/fx-workbench/internal/taskui"
	"github.com/jarneson/fx-workbench/internal/workbench"
	"github.com/jarneson/fx-workbench/internal/workload"
)

type source struct{}

func (source) Snapshot(context.Context) (workbench.Snapshot, error) { return workbench.Snapshot{}, nil }
func (source) Focus(context.Context, workbench.Session) error       { return nil }

type catalog struct{}

func (catalog) List(workload.ListOptions) ([]workload.Entity, error) { return nil, nil }

func TestTabSwitchesBetweenSiblingModes(t *testing.T) {
	model := NewModel(workbench.NewModel(source{}, nil, nil), taskui.NewModel(catalog{}, nil, nil, ""))
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	model = updated.(Model)
	if !strings.Contains(model.View(), "FX WORKBENCH") {
		t.Fatalf("live view missing:\n%s", model.View())
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyTab})
	model = updated.(Model)
	if !strings.Contains(model.View(), "WORKLOADS") {
		t.Fatalf("workload view missing:\n%s", model.View())
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'1'}})
	model = updated.(Model)
	if !strings.Contains(model.View(), "FX WORKBENCH") {
		t.Fatalf("numeric mode switch failed:\n%s", model.View())
	}
}
