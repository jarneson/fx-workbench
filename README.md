# FX Workbench

FX Workbench manages project plans, task packets, Git worktrees, and fx agent sessions in tmux. It includes:

- `fx-workbench`, a Bubble Tea cockpit and workload CLI
- `fx-principle`, which applies named engineering principles to an fx pane
- the tmux lifecycle bridge used by planner and task-agent panes
- project, planner, review, and cleanup popup helpers
- managed fx skills, agent profiles, and principles
- a small tmux fragment and portable tuicr defaults

## Prerequisites

Required:

- Go 1.24 or newer
- [fx](https://fx.sh/) on `PATH`
- tmux, Git, fzf, jq, Python 3.10 or newer, and Bash
- an Obsidian vault or another local directory that can store Markdown

Required for automatic JavaScript dependency installation:

- Node.js and npm for an unpinned `package.json`
- Corepack for projects pinned to pnpm or Yarn
- Bun for projects pinned to Bun

Optional:

- [tuicr](https://github.com/giacomo-b/rust-tuicr) for `prefix + v` reviews
- an authenticated [GitHub CLI](https://cli.github.com/) (`gh`) to open a task's pull request from the cockpit

The installer defaults to `~/.local/bin`; add that directory to `PATH` before using Workbench.

## Install

```bash
git clone https://github.com/jarneson/fx-workbench.git
cd fx-workbench
./install
```

The installer builds `fx-workbench` and `fx-principle`, then installs:

| Files | Default destination |
|---|---|
| Binaries and helper scripts | `~/.local/bin` |
| Managed fx assets | `~/.fx` |
| tmux fragment | `~/.config/fx-workbench/tmux.conf` |
| tuicr defaults | `~/.config/tuicr/config.toml` |

It builds and preflights every destination before writing. It does not replace a differing binary, helper script, or config file; move or remove the reported file and rerun the installer. It repairs the executable bit on an identical helper or binary. The managed fx asset installer likewise refuses unmanaged or user-edited files. Set `FX_WORKBENCH_MIGRATE_FX=1` only when you want conflicting legacy fx assets backed up and adopted.

Override destinations with `GOBIN`, `FX_WORKBENCH_FX_ROOT`, `FX_WORKBENCH_CONFIG_HOME`, `FX_WORKBENCH_TMUX_CONFIG`, or `FX_WORKBENCH_TUICR_CONFIG`.

To build without installing:

```bash
go build ./cmd/fx-workbench ./cmd/fx-principle
go test ./...
tests/install-smoke.sh
```

## Configure storage

Workbench stores canonical project, workstream, and task Markdown under:

```text
$OBSIDIAN_VAULT/Projects/<project>/
├── project.md
└── 01-<workstream>/
    ├── workstream.md
    └── 01-<task>.md
```

`OBSIDIAN_VAULT` defaults to `$HOME/Obsidian`. Set it in the environment inherited by tmux when your vault lives elsewhere:

```bash
export OBSIDIAN_VAULT="$HOME/Documents/Obsidian"
```

`fx-obs` stores the selected project under `${XDG_STATE_HOME:-$HOME/.local/state}/fx-workbench/obsidian-project`. A tmux pane can have its own selection, with the last selection as the default elsewhere.

Useful commands:

```bash
fx-obs projects
fx-obs set <project>
fx-obs current
fx-obs project-path
```

Create the first canonical project and workstream with a dry run before writing:

```bash
fx-workbench project onboard --dry-run \
  --id example --title "Example" \
  --root-repository "$PWD"

fx-workbench project onboard \
  --id example --title "Example" \
  --root-repository "$PWD"
```

## Configure tmux

Add this line to `~/.tmux.conf`, adjusting the path if you changed the config destination:

```tmux
source-file ~/.config/fx-workbench/tmux.conf
```

Reload tmux with `tmux source-file ~/.tmux.conf`.

| Binding | Action |
|---|---|
| `prefix + W` | Open the Workbench cockpit |
| `prefix + V` | Choose a project and focus its root session |
| `prefix + P` | Choose or resume a workstream planner |
| `prefix + N` | Create a workstream in the current project |
| `prefix + I` | Apply principles to the current fx pane |
| `prefix + v` | Review the current task with tuicr |
| `prefix + T` | Choose a branch worktree to delete |

The cleanup popup deletes the selected worktree and local branch. Read the selection before confirming it.

`fx-tmux` runs fx with a local lifecycle socket. `fx-tmux-statusd` translates fx lifecycle messages into tmux pane/window options, which lets Workbench show running, idle, blocked, and finished agents. Desktop notifications are intentionally outside this portable package.

## Task lifecycle

Canonical task states are:

```text
draft → ready → coding → self-review → in-review → merged
                    ↘ blocked
```

`superseded` closes work that will not merge. Workstreams and projects can be closed after their tasks are merged or superseded.

A normal flow is:

1. Create a task packet with `fx-workbench scaffold task`.
2. Set it ready with `fx-workbench status --project <project> --set ready <task>`.
3. Launch it from the cockpit or with `fx-workbench launch <task>`.
4. The worker edits and verifies its isolated Git worktree, then sets `self-review`.
5. Review with `prefix + v`; creating a ready pull request moves the task to `in-review`.
6. After the integrated commit is verified on the target branch, set `merged`.

Planner and task windows use the managed profiles in `~/.fx/agent-profiles`. The planner writes durable coordination state to the Markdown packets; repository files and Git remain the source of truth for implementation.

## Runtime configuration

| Variable | Purpose | Default |
|---|---|---|
| `OBSIDIAN_VAULT` | Root containing `Projects/` | `$HOME/Obsidian` |
| `FX_OBS_BIN` | `fx-obs` executable used by the Go CLI | `fx-obs` on `PATH` |
| `FX_TASK_WORKTREE_ROOT` | Parent directory for task worktrees | `$HOME/dev/worktrees` |
| `FX_TASK_INSTALL_COMMAND` | Explicit dependency command after creating a worktree | none |
| `FX_TASK_TMUX_BIN` | tmux executable used by task launch | `tmux` |
| `FX_TASK_FX_COMMAND` | command used for task-agent panes | `fx-tmux` |
| `FX_WORKBENCH_FX_TMUX` | command used for planner panes | `fx-tmux` |
| `FX_WORKBENCH_TMUX_BIN` | tmux executable used by planners and helpers | `tmux` |
| `FX_WORKBENCH_PROJECT` | Bypass the stored project selection for one command | unset |

For JavaScript repositories, task launch reads `package.json` and uses its pinned `packageManager`. A package without that field uses `npm install`. Repositories without `package.json` skip dependency installation. Set `FX_TASK_INSTALL_COMMAND` when a repository needs another command.

## Managed fx assets

Preview asset installation without writing:

```bash
fx-workbench fx install --dry-run --target "${FX_WORKBENCH_FX_ROOT:-$HOME/.fx}"
```

The public bundle contains only the planner and coding profiles, the principles those profiles reference, and the Obsidian planning and worktree launch skills. Private evaluation material is not included.

## License

MIT
