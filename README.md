<div align="center">

# jog

**a memory for your working tree**

*Automatic snapshots of your uncommitted work — untracked files included —<br>
stored as ordinary git objects. No daemon, no new VCS, no workflow change.*

</div>

---

```console
$ rm -rf src/parser        # oops. uncommitted changes and untracked files, gone.

$ jog snaps                # ...but every command boundary was snapshotted
c0ffee1  2 minutes ago   pre: git status
a1b2c3d  9 minutes ago   claude[b3f1a2c4]: Bash(go test ./...)
9e8d7c6  14 minutes ago  manual: before parser rewrite

$ jog back --all           # worktree back to the newest snapshot
restored to c0ffee1 (2 minutes ago — pre: git status): 14 restored, 0 deleted
(undo: jog back --all)
```

## Why jog

Pick the pitch that fits how you work:

- **A working-tree reflog.** Git has a safety net for refs; jog extends the
  same idea to your uncommitted changes. Every snapshot records *what command
  it ran ahead of* — the timeline is an operation log, not a smear of states.
- **The missing half of Claude Code's `/rewind`.** Claude's checkpoints
  restore Edit/Write changes but explicitly not bash-made changes, manual
  edits, or untracked files — and they expire. jog covers exactly that
  complement, at the same prompt/tool-call boundaries, with retention you
  control: `/rewind` the conversation, `jog back --all` the world.
- **[jj]'s snapshot model wearing git's skin.** Snapshot at the start of
  every command — but the commands are the git you already type.

[jj]: https://github.com/jj-vcs/jj

### How it works

- Snapshots are plain git blobs/trees/commits in your repo's own object
  database, on refs under `refs/jog/<branch>` — invisible to branches, the
  index, `git log`, teammates, and remotes. Unchanged files cost zero bytes.
  There is no jog database.
- Snapshots are **command-triggered, causally before the command runs** — the
  only ordering that protects against `reset --hard` and `checkout -f`. No
  daemon, no filesystem watcher, no git hooks to install or break.
- Three triggers: the shell alias (every git command you type), Claude Code
  hooks (every prompt and tool call), and `jog` itself (deliberate
  checkpoints). A no-op snapshot costs a few tens of milliseconds.
- jog never reimplements a git verb, and never touches your index, HEAD,
  branches, or config. If jog vanished tomorrow, stock git reads every
  snapshot ([see below](#no-lock-in)).

> [!NOTE]
> jog is v0 and under active daily dogfood. The snapshot engine's behavior is
> lab-verified against git 2.50; every verified fact ships as a test.

## Install

**1. Install the binary** (Go 1.26+; brew tap planned for v1):

```sh
go install github.com/tyler-johnson/jog/cmd/jog@latest
```

**2. Add the alias** — this is how you "remember" to snapshot: you don't.
Muscle memory is the trigger; the compulsive `git status` tic becomes the
snapshot heartbeat.

```sh
# bash / zsh (~/.bashrc / ~/.zshrc)
alias git='jog git'

# fish
alias git 'jog git'
```

Scripts, IDEs, and CI are untouched — they resolve `git` on PATH and get real
git. That's a feature: jog stays out of every code path that expects exact
git behavior.

**3. Wire up Claude Code** (optional) — user-level `~/.claude/settings.json`
covers every repo with zero per-repo setup; the hook exits in milliseconds
outside git repos and never blocks a tool call:

```json
{
  "hooks": {
    "PreToolUse": [{
      "matcher": "Bash|Edit|Write|NotebookEdit",
      "hooks": [{ "type": "command", "command": "jog hook claude" }]
    }],
    "UserPromptSubmit": [{
      "hooks": [{ "type": "command", "command": "jog hook claude" }]
    }]
  }
}
```

(If `~/go/bin` isn't on your PATH for non-interactive shells, use the
absolute path, e.g. `/home/you/go/bin/jog hook claude`.)

**4. Verify:** make any change in a repo, run `git status`, then `jog snaps`
— you should see a `pre: git status` entry.

## Usage

Two disjoint namespaces, one rule: **`jog git` is the only door to git.**

| command | what it does |
|---|---|
| `jog` | snapshot now, show the top of the timeline |
| `jog -m "msg"` | snapshot with a message (`manual: msg`) |
| `jog snaps [-p] [--all] [path…]` | the timeline: id, age, provenance, files changed (`-p`: patches, `--all`: every branch, interleaved) |
| `jog since [T] [path…]` | what changed since a snapshot (default: your last command boundary; `-p`: patches) |
| `jog back <path>… [--at T]` | restore files from a snapshot (worktree only) |
| `jog back --all [--at T]` | restore the whole tree, including deleting files created since |
| `jog git <args>` | snapshot, then run the real git command — what the alias expands to |
| `jog version` | print jog's version |

`--at` (and `since`'s target slot) accepts a snap id from `jog snaps` or
git's reflog time syntax: `--at 20.minutes.ago`, `--at yesterday`. Asking
for a time older than the oldest snapshot falls back to the oldest, with a
warning.

A reading rule for the timeline: provenance records **what jog was running
ahead of**, never who made the changes. Manual edits swept up by an
agent-triggered snapshot are attributed to that trigger — jog can't know who
typed between boundaries, and refuses to guess.

`pick`, `trim`, `mcp`, and `doctor` are reserved for future releases.
Anything else is an error — jog never guesses.

## Recovery cookbook

**An agent deleted a file three prompts ago.** Find it, get it back:

```console
$ jog snaps src/config.yaml
f3a9b12  8 minutes ago  claude[b3f1a2c4]: Bash(rm -rf src/old)
D       src/config.yaml
$ jog back src/config.yaml --at 9.minutes.ago
restored src/config.yaml from 2c4d6e8 (9 minutes ago — claude[b3f1a2c4]: prompt "clean up the src tree")
```

**Roll everything back to before a refactor went sideways** — including
deleting the files it scattered:

```console
$ jog back --all --at 25.minutes.ago
restored to 9e8d7c6 (26 minutes ago — manual: before parser rewrite): 11 restored, 3 deleted
(undo: jog back --all)
```

Every restore snapshots first, so undo is itself undoable.

**What did I actually change in the last hour?**

```console
$ jog snaps -p           # full patches, in your pager
$ jog snaps src/parser/  # just one path's history
```

## What jog will never touch

1. Your **index** — byte-identical across any jog operation (jog stages into
   a private shadow index; all reads use `--no-optional-locks`).
2. Your **worktree** — written only by an explicit `jog back`, which is
   itself snapshotted first.
3. **HEAD, branches, tags, remotes** — never written.
4. **Repo config** — two `gc.refs/jog/*` keys are set once, on first
   snapshot, so git's gc never expires jog's history; announced when it
   happens.

Crash at any point leaves at worst orphan objects for git's gc to sweep —
jog only adds objects and moves its own refs, with compare-and-swap updates
so concurrent triggers can't clobber each other.

### No lock-in

Every snapshot is recoverable with stock git alone:

```sh
git log refs/jog/main                                   # browse the chain
git show 'refs/jog/main@{2.hours.ago}:src/main.go'      # read one file
git restore --source refs/jog/main --worktree -- path   # restore one file
```

## Why not jog

Honesty section, in the ripgrep tradition. jog does **not** protect against:

- **GUI discards.** VS Code's git panel, Fork, etc. call git via their own
  bindings — no trigger fires. (VS Code moves discarded files to the trash,
  which is some comfort.)
- **Scripts and CI running real git.** They bypass the alias *by design*.
- **Ignored files.** Never snapshotted, by design — jog is not a backup
  system for `node_modules` or build artifacts.
- **Solo terminal work without the alias installed.** No trigger, no snapshot.

Also worth knowing:

- `refs/jog/*` stays private through normal `push`/`fetch`/`clone`, but
  **leaks** via `push --mirror`, `clone --mirror`, explicit `refs/*:refs/*`
  refspecs, and `git bundle --all`. Your snapshots contain your scratch work
  — know your mirror scripts.
- v0 has no retention yet: snapshots accumulate until `jog trim` lands in v1.
  Space cost is low (content-addressed, delta-compressed by repack), but the
  timeline gets long.
- New files over 50 MiB are skipped (configurable, see below) and listed in
  the timeline entry.

## Configuration

Native `git config` keys — global or per-repo, no new file format:

| key | default | meaning |
|---|---|---|
| `jog.maxFileSize` | `50m` | skip new files larger than this (`0` disables the guard) |
| `gc.refs/jog/*.reflogExpire` | `never` | set by jog on first snapshot; keeps gc off jog's reflogs |
| `gc.refs/jog/*.reflogExpireUnreachable` | `never` | same |

## How it compares

- **[jj]** — the inspiration for snapshot-on-every-command, but it's a new
  VCS with a workflow tax, and it's incompatible with `git worktree`. jog is
  git, plus a memory.
- **[dura](https://github.com/tkellogg/dura)** — background daemon, hash-named
  branches that made recovery painful; dormant.
- **[gitwatch](https://github.com/gitwatch/gitwatch)** — commits your real
  branch on a watcher; pollutes history.
- **Editor local history** (VS Code, JetBrains) — blind to terminal and
  agent changes, short retention, not in git.
- **Claude Code checkpoints** — conversation + Edit/Write only; no bash
  changes, no untracked files, capped at 100 checkpoints / 30 days. jog is
  the other half.

## Roadmap

- **v1:** `since` (what changed vs N ago), `pick` (interactive TUI scrubber),
  `trim` + tapering retention (hourly ≤ 7d, daily ≤ 90d), `doctor`, brew tap.
- **v2:** MCP server (agents query their own snapshot history), optional
  encrypted backup push of `refs/jog/*` to a private remote, shell prompt
  segment.

## License

[MIT](LICENSE)
