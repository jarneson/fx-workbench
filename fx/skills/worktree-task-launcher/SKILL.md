---
name: worktree-task-launcher
description: Launch a canonical FX Workbench task in its own Git worktree and tmux window with an interactive fx agent. Use when asked to fork, launch, delegate, or start a task from a durable packet.
---

# Worktree task launcher

Launch one canonical task packet through FX Workbench. The command owns branch, worktree, tmux, dependency, and prompt delivery decisions; do not recreate those steps in shell glue.

## Inputs

Resolve these before launching:

- A canonical task packet, located by stable task ID.
- The project when the task ID is not globally unambiguous.
- An optional branch override when the user does not want the branch stored in the packet.

If the request is only an informal description, use the `obsidian-planner` workflow to create a self-contained task packet first. Do not launch from chat-only context.

## Preflight and launch

Read the packet and inspect the target repository before launching. Confirm its repository, branch, assumptions, and current status are still correct. Never reset, delete, or reuse a dirty worktree to make preflight pass.

Run the non-mutating preflight first:

```bash
fx-workbench launch --dry-run [--project <project>] [--branch <branch>] <task-id>
```

If the plan is correct, launch through the same command:

```bash
fx-workbench launch [--project <project>] [--branch <branch>] <task-id>
```

Workbench then:

1. Resolves the canonical packet, repository, branch, worktree, workstream session, and coding profile.
2. Requires the main checkout to be clean, fetches `origin/main`, fast-forwards local `main`, and verifies equality.
3. Creates a new linked worktree under `${FX_TASK_WORKTREE_ROOT:-$HOME/dev/worktrees}`.
4. Starts dependency installation and the task fx agent concurrently.
5. Creates a task window in the workstream's tmux session and waits for the fx lifecycle bridge to become ready.
6. Sends the coding profile, complete task packet, and worker guidance as one prompt.
7. Records dependency completion or failure on the task pane and reports any partial resources if launch fails.

Repositories without `package.json` skip dependency installation. JavaScript repositories use the pinned `packageManager`, or `npm install` when it is absent. `FX_TASK_INSTALL_COMMAND` overrides automatic installation for repositories with another setup command.

## Safety and handoff

- Treat branch, worktree, task-window, and session identity conflicts as blockers. Do not delete or repurpose existing resources automatically.
- The launched agent works only in the new task worktree and must not create another branch or worktree.
- The launched agent must not commit, push, or open a pull request unless the user explicitly requests it.
- Wait for `tmux show-options -p -v @fx_install_state` to report `complete` before dependency-sensitive commands. If it reports `failed`, diagnose that installation before continuing.
- Keep the task packet as durable coordination state and update status through `fx-workbench status`.
