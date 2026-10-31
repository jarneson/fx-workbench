package appcli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"

	fxassets "github.com/jarneson/fx-workbench/fx"
)

func runFXAssets(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: fx-workbench fx install [--dry-run] [--target <fx-root>] [--json]")
	}
	if args[0] != "install" {
		return fmt.Errorf("unknown fx command: %s", args[0])
	}
	flags := flag.NewFlagSet("fx install", flag.ContinueOnError)
	flags.SetOutput(stderr)
	target := flags.String("target", "", "fx config root (defaults to ~/.fx)")
	dryRun := flags.Bool("dry-run", false, "show changes without writing")
	migrateLegacy := flags.Bool("migrate-legacy", false, "back up and adopt unmanaged files at Workbench-owned paths")
	jsonOutput := flags.Bool("json", false, "emit JSON")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %s", flags.Arg(0))
	}
	result, err := fxassets.Install(fxassets.InstallOptions{Target: *target, DryRun: *dryRun, MigrateLegacy: *migrateLegacy})
	if err != nil {
		return err
	}
	if *jsonOutput {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}
	mode := "installed"
	if *dryRun {
		mode = "would install"
	}
	fmt.Fprintf(stdout, "fx root: %s\n", result.Target)
	for _, path := range result.Installed {
		fmt.Fprintf(stdout, "%s: %s\n", mode, path)
	}
	for _, path := range result.Unchanged {
		fmt.Fprintf(stdout, "unchanged: %s\n", path)
	}
	for _, path := range result.BackedUp {
		fmt.Fprintf(stdout, "legacy backup: %s\n", path)
	}
	if result.BackupRoot != "" {
		fmt.Fprintf(stdout, "backup root: %s\n", result.BackupRoot)
	}
	return nil
}
