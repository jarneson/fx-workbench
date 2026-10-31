package workbench

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

type fakeSource struct{ snapshot Snapshot }

func (f *fakeSource) Snapshot(context.Context) (Snapshot, error) { return f.snapshot, nil }
func (f *fakeSource) Focus(context.Context, Session) error       { return nil }

type fakeDiscovery struct{ workspaces []Workspace }

func (f *fakeDiscovery) Discover(context.Context) ([]Workspace, error) { return f.workspaces, nil }

type fakeLauncher struct {
	opened []Workspace
	result OpenResult
}

func (f *fakeLauncher) OpenWorkspace(_ context.Context, workspace Workspace) (OpenResult, error) {
	f.opened = append(f.opened, workspace)
	return f.result, nil
}

func TestLaunchPaletteFiltersAndOpensThroughCommand(t *testing.T) {
	discovery := &fakeDiscovery{workspaces: []Workspace{
		{Kind: WorkspaceRepository, Label: "widgets", Path: "/code/widgets", Repository: "/code/widgets"},
		{Kind: WorkspaceWorktree, Label: "widgets/fix parser", Path: "/code/worktrees/fix parser", Repository: "/code/widgets"},
	}}
	launcher := &fakeLauncher{}
	model := NewModel(&fakeSource{}, discovery, launcher)
	model.width, model.height = 100, 30

	updated, command := model.Update(key('o'))
	model = updated.(Model)
	if !model.palette || command == nil || len(launcher.opened) != 0 {
		t.Fatalf("palette did not open asynchronously: %#v", model)
	}
	updated, _ = model.Update(command())
	model = updated.(Model)
	for _, char := range "parser" {
		updated, _ = model.Update(key(char))
		model = updated.(Model)
	}
	filtered := model.filteredWorkspaces()
	if len(filtered) != 1 || filtered[0].Kind != WorkspaceWorktree {
		t.Fatalf("filtered workspaces = %#v", filtered)
	}
	view := model.View()
	if !strings.Contains(view, "TREE") || !strings.Contains(view, "widgets/fix parser") {
		t.Fatalf("palette view missing typed worktree:\n%s", view)
	}

	updated, command = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if command == nil || len(launcher.opened) != 0 || !model.launching {
		t.Fatalf("open mutation ran inside Update: opened=%#v launching=%v", launcher.opened, model.launching)
	}
	_ = command()
	if len(launcher.opened) != 1 || launcher.opened[0].Path != "/code/worktrees/fix parser" {
		t.Fatalf("opened = %#v", launcher.opened)
	}
}

func TestLaunchPaletteCancellationDoesNotMutate(t *testing.T) {
	discovery := &fakeDiscovery{workspaces: []Workspace{{Kind: WorkspaceRepository, Label: "widgets", Path: "/code/widgets"}}}
	launcher := &fakeLauncher{}
	model := NewModel(&fakeSource{}, discovery, launcher)
	model.palette = true
	model.workspaces = discovery.workspaces

	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = updated.(Model)
	if model.palette || command != nil || len(launcher.opened) != 0 {
		t.Fatalf("cancel mutated state: palette=%v command=%v opened=%#v", model.palette, command != nil, launcher.opened)
	}
}

func key(char rune) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{char}} }
