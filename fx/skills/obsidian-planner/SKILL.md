---
name: obsidian-planner
description: Maintain durable technical plans and forkable agent task packets in the user's local Obsidian vault. Use when asked to plan a project, create or refine a backlog, hand work to later agents, coordinate parallel worktrees, record substantial investigation findings, or run an ongoing planning agent.
---

# Obsidian planner

Use the local Obsidian vault as durable coordination state. Repository code and local evidence remain the source of truth.

## Select the logical project

Obsidian is a local filesystem vault. Do not query MCP, search `$HOME` for `.obsidian`, inspect Obsidian application configuration, or otherwise rediscover its location.

1. Run `~/.local/bin/fx-obs current` before any vault read or write. It resolves a pane-specific selection, then the global last-used project.
2. If the user names a logical project, run `~/.local/bin/fx-obs set <project>` directly.
3. If no project is named and current is unset, run `~/.local/bin/fx-obs projects` and ask the user to choose an existing logical project before creating a new one.
4. Run `~/.local/bin/fx-obs project-path` for the exact readable project root.
5. Run `~/.local/bin/fx-obs path` for the exact writable project root.
6. Use those returned paths directly with file tools. Do not search for the vault or project directory.

Read legacy notes and relevant project-level notes before planning. Do not rewrite or reorganize old notes merely to normalize them.

## Planning workflow

1. Inspect current repository state, relevant implementation, tests, documentation, and existing notes.
2. Separate verified facts from assumptions and open questions.
3. Identify dependencies and boundaries that allow tasks to run in independent worktrees.
4. Search existing task packets before creating new ones. Update matching packets rather than duplicating work.
5. Resolve the project and workstream. Projects may span repositories; every executable task names one absolute target repository.
6. Write one overview note when the plan needs shared context.
7. Create canonical workstreams and tasks with `~/.local/bin/fx-workbench scaffold`, then fill in their Markdown bodies. Do not hand-invent frontmatter.
8. Do not implement tasks while acting as the planner unless the user explicitly asks.

Use these commands as the structured workload API:

```bash
fx-workbench project onboard --dry-run --id <project> --title <title> --root-repository <absolute-root-repo> [--repo <absolute-repo> ...]
fx-workbench project onboard --id <project> --title <title> --root-repository <absolute-root-repo> [--repo <absolute-repo> ...]
fx-workbench project migrate-layout --dry-run --project <project>
fx-workbench project close --dry-run <project>
fx-workbench project close --yes [--close-sessions] <project>
fx-workbench project reopen --yes <project>
fx-workbench list --type workstream --project <project>
fx-workbench list --type task --project <project> [--workstream <id>] [--status <status>]
fx-workbench scaffold workstream --project <project> --id <id> --title <title> --repository <absolute-root-repo>
fx-workbench scaffold task --project <project> --workstream <id> --id <id> --title <title> --repository <absolute-repo> [--branch <branch>] [--objective <objective>]
fx-workbench status --project <project> --set <status> <task-id>
fx-workbench status --project <project> --set <active|closed> <workstream-id>
```

Obsidian Markdown remains authoritative. `fx-workbench` normalizes legacy packets for reading, but new entities use `schema: fx-workbench/v1`, stable IDs, and explicit relationships. Do not use filenames or `tasks/completed/` placement as identity or status. For an existing canonical workstream, onboard with `--from-project <id> --migrate-workstream <id>` only after an identical `--dry-run`; the command leaves redirect notes at old paths.

Keep ongoing planning in the parent session and use Obsidian packets as the durable cross-session handoff. On fx 0.0.8, do not keep a named persistent planning subagent across interruptions or recovery; stale child lifecycle state can invalidate later tool calls. Use one-off subagents only for bounded research that can safely finish within the current turn.

## Task packet format

Use `fx-workbench scaffold` to allocate paths. Workstreams are project-root folders such as `01-jobs-on-hive/`; their overview always uses the fixed name `workstream.md`, while tasks are numbered locally, for example `01-jobs-on-hive/01-bound-request-size.md`. Never add date prefixes manually.

```markdown
---
schema: fx-workbench/v1
type: task
id: bound-joh-request-size
title: Bound request size
status: ready
project: example-ci
workstream: service-hardening
repository: /absolute/path/to/repository
branch: bound-joh-request-size
created: 2026-09-10T00:00:00Z
updated: 2026-09-10T00:00:00Z
depends_on: []
---

# Bound jobs-on-hive request size

## Objective

One concrete outcome.

## Context and evidence

- Verified facts with source paths, line numbers, PRs, or URLs.
- Relevant `[[wikilinks]]`.

## Scope

- Included files or components.
- Explicit non-goals.

## Constraints

- Repository invariants, compatibility requirements, and risks.

## Acceptance criteria

- Observable success and failure behavior.

## Validation

- Focused checks that prove the behavior.

## Handoff prompt

A standalone prompt a fresh agent can execute without relying on chat history.
```

Canonical task statuses are `draft`, `blocked`, `ready`, `coding`, `self-review`, `in-review`, `merged`, and `superseded`. `active`, `done`, and `ready-for-review` are legacy read-compatible states that should migrate to `coding` and `self-review`. Launching a ready task marks it `coding`; the authoring agent marks it `self-review` after implementation and local validation; local `tuicr` review does not advance status; the task moves to `in-review` immediately after a ready-for-review pull request is created; `merged` means the integrated change is on the target branch and stable. Projects and workstreams additionally use `closed` after guarded closeout. Change task/workstream status through `fx-workbench status` and project lifecycle through `fx-workbench project close|reopen`; workstream close requires every child task to be merged or superseded. Do not move files between status directories. `id`, `project`, `workstream`, and `depends_on` contain stable IDs, while `repository` is an absolute local checkout path. Record blockers and required decisions in the body.

## Packet quality

- A packet must be executable by a fresh agent with no parent transcript.
- Include likely repository and worktree, but require the executor to verify current paths and branch state.
- Keep task titles at 20 characters or fewer so they remain readable in the live sidebar. Put detail in the objective, not the title.
- Keep tasks reviewable and independently verifiable. Avoid vague packets such as “finish migration.”
- Do not prescribe implementation details that have not been validated against source.
- Mark dependencies explicitly and avoid parallel tasks that edit the same files unless sequencing is intentional.
- Keep credentials, private tokens, and copied chat transcripts out of notes.

## Consuming a task

When asked to execute a packet:

1. Re-read the packet and validate its assumptions against current source.
2. Ensure the task is `coding` before editing; canonical launch does this automatically.
3. Implement and verify in the target repository.
4. Add concise outcomes, deviations, and follow-ups to the packet.
5. Run `fx-workbench status --set self-review <task-id>` after implementation and local validation; otherwise set `blocked` and state the blocker.
6. Keep the task in `self-review` during local `tuicr` review. Create pull requests ready for review rather than as drafts. Immediately run `fx-workbench status --set in-review <task-id>` after PR creation succeeds, ask the user which reviewer to request, and add that reviewer without guessing.
7. Run `fx-workbench status --set merged <task-id>` only after verifying the integrated commit is present on the target branch (and remote target when applicable).
