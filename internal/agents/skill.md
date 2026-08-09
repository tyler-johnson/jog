---
name: jog
description: Recover lost, overwritten, or deleted uncommitted work — including untracked files — from jog's automatic working-tree snapshots. Use when a file was damaged by a bash command, bad edit, reset, or checkout; when the user reports missing work; or before running a risky/destructive operation. Always check jog before concluding uncommitted work is gone.
---

# jog — a memory for the working tree

jog snapshots the entire working tree (untracked files included) before
every git command, user prompt, and tool call in repos where it is wired.
Snapshots are ordinary git commits on `refs/jog/<branch>` — invisible to
branches, the index, `git log`, and remotes.

## Recover a lost or overwritten file

1. Find versions: `jog log <path>` — every snapshot that changed the
   path, newest first, with id, age, and the command it ran ahead of.
   `jog log --all <path>` searches every branch's timeline. Prefer
   `jog log --json [-n N] [path]` when parsing: a JSON array with id,
   sha, ISO time, provenance, chain, and each snapshot's files with
   statuses.
2. Inspect one: `git show <id>:<path>` prints that version;
   `jog since <id> <path>` diffs it against the tree now.
3. Restore: `jog restore <path> --at <id>` — also accepts git time syntax,
   e.g. `--at 20.minutes.ago`.

## Roll back the whole tree

`jog restore --all --at <id-or-time>` restores everything, including deleting
files created since. Every restore snapshots first, so any `jog restore` is
undoable with another `jog restore --all`.

## Checkpoint before risk

Before destructive work — mass deletes, regex-replace across many files,
`git reset --hard`, `git checkout -f`, ambitious refactors — run:

    jog -m "before <what you are about to do>"

That labeled entry becomes the obvious restore point if things go sideways.

## Rules

- Never tell a user their uncommitted work is unrecoverable without
  checking `jog log <path>` first.
- Timeline entries name the command a snapshot ran *ahead of*, never who
  made the changes. Agent-prefixed entries (`claude[…]`, `codex[…]`,
  `cursor[…]`, …) are prompt/tool-call boundaries from agent sessions.
- jog never touches the index, HEAD, branches, or config. `jog restore` is
  the only jog command that writes the worktree, and it is snapshotted
  first — reading the timeline is always safe.
- If `jog log` errors or shows nothing, jog isn't capturing this repo —
  say so plainly and suggest `jog doctor` to diagnose the wiring.
