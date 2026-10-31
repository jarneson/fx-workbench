package appcli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/jarneson/fx-workbench/internal/principles"
	"github.com/jarneson/fx-workbench/internal/promptdelivery"
)

func runPrinciple(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: fx-workbench principle <list|show|render|apply|pick> [flags]")
	}
	switch args[0] {
	case "list":
		return runPrincipleList(args[1:], stdout, stderr)
	case "show":
		return runPrincipleShow(args[1:], stdout, stderr)
	case "render":
		return runPrincipleRender(args[1:], stdout, stderr)
	case "apply":
		return runPrincipleApply(args[1:], stdout, stderr)
	case "pick":
		return runPrinciplePick(args[1:], stdout, stderr)
	default:
		return fmt.Errorf("unknown principle command: %s", args[0])
	}
}

func runPrincipleList(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("principle list", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", "", "principle directory")
	jsonOutput := flags.Bool("json", false, "emit JSON")
	format := flags.String("format", "table", "table or tsv")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %s", flags.Arg(0))
	}
	items, err := principles.New(*root).List()
	if err != nil {
		return err
	}
	if *jsonOutput {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(items)
	}
	if *format == "tsv" {
		for _, item := range items {
			fmt.Fprintf(stdout, "%s\t%s\t%s\n", item.ID, item.Title, item.Description)
		}
		return nil
	}
	if *format != "table" {
		return fmt.Errorf("invalid format: %s", *format)
	}
	writer := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "ID\tTITLE\tDESCRIPTION")
	for _, item := range items {
		fmt.Fprintf(writer, "%s\t%s\t%s\n", item.ID, item.Title, item.Description)
	}
	return writer.Flush()
}

func runPrincipleShow(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("principle show", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", "", "principle directory")
	jsonOutput := flags.Bool("json", false, "emit JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("usage: fx-workbench principle show [--root <dir>] <id>")
	}
	item, err := principles.New(*root).Load(flags.Arg(0))
	if err != nil {
		return err
	}
	if *jsonOutput {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(item)
	}
	fmt.Fprintf(stdout, "%s", item.Body)
	if !strings.HasSuffix(item.Body, "\n") {
		fmt.Fprintln(stdout)
	}
	return nil
}

func runPrincipleRender(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("principle render", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", "", "principle directory")
	jsonOutput := flags.Bool("json", false, "emit JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	bundle, err := principles.New(*root).Render(flags.Args())
	if err != nil {
		return err
	}
	if *jsonOutput {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(bundle)
	}
	fmt.Fprint(stdout, bundle.Text)
	return nil
}

func runPrincipleApply(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("principle apply", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", "", "principle directory")
	targetPane := flags.String("target-pane", os.Getenv("FX_PRINCIPLE_TARGET_PANE"), "target fx pane")
	tmuxBinary := flags.String("tmux-bin", envDefault("FX_PRINCIPLE_TMUX_BIN", "tmux"), "tmux executable")
	if err := flags.Parse(args); err != nil {
		return err
	}
	bundle, err := principles.New(*root).Render(flags.Args())
	if err != nil {
		return err
	}
	if err := applyPrinciples(context.Background(), *tmuxBinary, *targetPane, bundle, promptdelivery.Pause); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "applied: %s\ndigest: %s\ntarget pane: %s\n", strings.Join(bundle.IDs, ", "), bundle.Digest, *targetPane)
	return nil
}

func runPrinciplePick(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("principle pick", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", "", "principle directory")
	targetPane := flags.String("target-pane", os.Getenv("FX_PRINCIPLE_TARGET_PANE"), "target fx pane")
	tmuxBinary := flags.String("tmux-bin", envDefault("FX_PRINCIPLE_TMUX_BIN", "tmux"), "tmux executable")
	fzfBinary := flags.String("fzf-bin", envDefault("FX_PRINCIPLE_FZF_BIN", "fzf"), "fzf executable")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %s", flags.Arg(0))
	}
	items, err := principles.New(*root).List()
	if err != nil {
		return err
	}
	if len(items) == 0 {
		return errors.New("no principles installed")
	}
	var input strings.Builder
	for _, item := range items {
		fmt.Fprintf(&input, "%s\t%s\t%s\n", item.ID, item.Title, item.Description)
	}
	executable, _ := os.Executable()
	previewCommand := " principle show"
	if filepath.Base(executable) == "fx-principle" {
		previewCommand = " show"
	}
	preview := shellQuote(executable) + previewCommand + " --root " + shellQuote(principles.New(*root).Root) + " {1}"
	command := exec.Command(*fzfBinary, "--multi", "--delimiter=\t", "--with-nth=2,1,3", "--layout=reverse", "--border=rounded", "--prompt=principles > ", "--header=Tab: select  ·  Enter: apply  ·  Esc: cancel", "--preview", preview)
	command.Stdin = strings.NewReader(input.String())
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && (exitErr.ExitCode() == 1 || exitErr.ExitCode() == 130) {
			return nil
		}
		return fmt.Errorf("principle picker: %w", err)
	}
	var ids []string
	for _, line := range strings.Split(strings.TrimSpace(output.String()), "\n") {
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, "\t", 2)
		ids = append(ids, fields[0])
	}
	if len(ids) == 0 {
		return nil
	}
	bundle, err := principles.New(*root).Render(ids)
	if err != nil {
		return err
	}
	if err := applyPrinciples(context.Background(), *tmuxBinary, *targetPane, bundle, promptdelivery.Pause); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "applied: %s\ndigest: %s\ntarget pane: %s\n", strings.Join(ids, ", "), bundle.Digest, *targetPane)
	return nil
}

func applyPrinciples(ctx context.Context, tmuxBinary, pane string, bundle principles.Bundle, pause func(context.Context, time.Duration) error) error {
	if pane == "" {
		return errors.New("--target-pane is required")
	}
	deadOutput, err := exec.CommandContext(ctx, tmuxBinary, "display-message", "-p", "-t", pane, "#{pane_dead}").CombinedOutput()
	if err != nil {
		return fmt.Errorf("inspect target pane: %s", commandOutput(deadOutput, err))
	}
	if strings.TrimSpace(string(deadOutput)) != "0" {
		return fmt.Errorf("target pane is dead: %s", pane)
	}
	sessionOutput, err := exec.CommandContext(ctx, tmuxBinary, "show-options", "-p", "-v", "-t", pane, "@fx_session_id").CombinedOutput()
	if err != nil || strings.TrimSpace(string(sessionOutput)) == "" {
		return fmt.Errorf("target is not an identified fx pane: %s", pane)
	}
	buffer := "fx-principle-" + strconv.Itoa(os.Getpid()) + "-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	if err := promptdelivery.Paste(ctx, promptdelivery.Exec(tmuxBinary), buffer, pane, []byte(bundle.Text)); err != nil {
		return fmt.Errorf("deliver principle: %w", err)
	}
	if err := pause(ctx, promptdelivery.SubmitDelay); err != nil {
		return fmt.Errorf("deliver principle: pause before submit: %w", err)
	}
	if output, err := exec.CommandContext(ctx, tmuxBinary, "send-keys", "-t", pane, "Enter").CombinedOutput(); err != nil {
		_, _ = exec.CommandContext(context.Background(), tmuxBinary, "delete-buffer", "-b", buffer).CombinedOutput()
		return fmt.Errorf("deliver principle: %s", commandOutput(output, err))
	}
	return nil
}

func commandOutput(output []byte, err error) string {
	message := strings.TrimSpace(string(output))
	if message != "" {
		return message
	}
	return err.Error()
}

func envDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
