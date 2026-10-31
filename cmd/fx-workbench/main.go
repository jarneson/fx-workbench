package main

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/jarneson/fx-workbench/internal/appcli"
	"github.com/jarneson/fx-workbench/internal/cockpit"
	"github.com/jarneson/fx-workbench/internal/taskui"
	"github.com/jarneson/fx-workbench/internal/workbench"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "tasks" {
		if err := taskui.Run(os.Args[2:], os.Stderr); err != nil {
			fmt.Fprintf(os.Stderr, "fx-workbench tasks: %v\n", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "sidebar" {
		if err := taskui.RunSidebar(os.Args[2:], os.Stderr); err != nil {
			fmt.Fprintf(os.Stderr, "fx-workbench sidebar: %v\n", err)
			os.Exit(1)
		}
		return
	}
	if code := appcli.Run(os.Args[1:], os.Stdout, os.Stderr); code >= 0 {
		os.Exit(code)
	}

	tmux := workbench.NewTmuxSource("")
	home, _ := os.UserHomeDir()
	discovery := workbench.NewLocalDiscovery(workbench.DefaultDiscoveryPolicy(home))
	live := workbench.NewModel(tmux, discovery, tmux)
	workloads := taskui.NewDefaultModel("", tmux)
	model := cockpit.NewModel(live, workloads)
	// A tmux popup is already an isolated drawing surface. Entering a nested
	// alternate screen makes tmux batch frames and introduces visible input lag.
	if _, err := tea.NewProgram(model, tea.WithFPS(120)).Run(); err != nil {
		fmt.Fprintf(os.Stderr, "fx-workbench: %v\n", err)
		os.Exit(1)
	}
}
