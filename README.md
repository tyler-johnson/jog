<div align="center">

# jog

**a memory for your working tree**

*Automatic snapshots of your uncommitted work — untracked files included —<br>
stored as ordinary git objects. No daemon, no new VCS, no workflow change.*

</div>

---

```console
$ rm -rf src/parser        # oops. uncommitted changes and untracked files, gone.

$ jog log                  # ...but every command boundary was snapshotted
c0ffee1  deadbee  2 minutes ago   pre: git status
a1b2c3d  deadbee  9 minutes ago   claude[b3f1a2c4]: Bash(go test ./...)
9e8d7c6  deadbee  14 minutes ago  manual: before parser rewrite

$ jog restore --all        # worktree back to the newest snapshot
restored to c0ffee1 (2 minutes ago — pre: git status): 14 restored, 0 deleted
(undo: jog restore --all)
```

- [Why jog](#why-jog)
- [Install](#install)
- [Recovery cookbook](#recovery-cookbook)
- [Usage](#usage)
- [Hooks](#hooks)
  - [Shell](#shell)
  - [Agents](#agents)
  - [Editors](#editors)
- [What jog will never touch](#what-jog-will-never-touch)
- [Why not jog](#why-not-jog)
- [Configuration](#configuration)
- [How it compares](#how-it-compares)

## Why jog

_**Think of jog as an insurance policy for your working tree: invisible until you need it,
invaluable when you do.**_

Inspired largely by [jj], jog is a snapshotting tool for uncommitted changes in
Git working trees. It's designed to run silently inside the workflow you already
have, capturing changes as they happen. With this, any mistake against the
working tree becomes instantly recoverable.

Git protects your commits, but it has never protected your working tree. Losing
uncommitted work to a wrong command is never fun, and now coding agents have
made it commonplace. Jog aims to solve this.

[jj]: https://github.com/jj-vcs/jj

### How it works

- **Snapshots are plain git blobs/trees/commits** in your repo's own object
  database, on refs under `refs/jog/<branch>` — invisible to branches, the
  index, `git log`, teammates, and remotes. Unchanged files cost zero bytes.
  There is no jog database.
- **No daemon, no filesystem watcher, no git hooks to install or break.**
  jog aims for robust over perfect: you don't need high-fidelity changesets of
  everything you type — you need a place to restore to when a mistake is
  made.
- **Designed to be totally invisible.** jog piggybacks the workflow you already
  have, snapshotting as you work. Every git repo is covered automatically with
  nothing to enable, and it maintains itself, so there is nothing to clean up.
  If you use agents, you don't even need to remember jog exists — when a
  mistake is made, the agent will.

## Install

### Install script

```sh
# linux / macOS — verifies checksums, lands in ~/.local/bin
curl -fsSL https://raw.githubusercontent.com/tyler-johnson/jog/main/install.sh | sh
```

```powershell
# Windows (PowerShell) — installs and adds jog to your user PATH
irm https://raw.githubusercontent.com/tyler-johnson/jog/main/install.ps1 | iex
```

The script ends by launching `jog install` — the guided setup described
below.

Script installs also **keep themselves up to date**: jog checks for new
releases daily in the background and installs them silently — install
once and forget it. `jog config autoUpdate false` prints a one-line
notice instead of installing, and `jog config updateCheck false` opts
out of all of it.

### Other ways to install

```sh
# Homebrew (macOS / Linux)
brew install tyler-johnson/tap/jog

# or with Go 1.26+
go install github.com/tyler-johnson/jog/cmd/jog@latest
```

Prebuilt binaries for linux/darwin/windows (amd64/arm64) are on the
[releases page](https://github.com/tyler-johnson/jog/releases). Brew and
go installs update through their own tools (`brew upgrade jog` /
`go install …@latest`) — jog prints a one-line notice when a new release
exists and says which command applies.

However jog got onto the machine, finish by running `jog install` — a
few questions that wire up your shell, agents, and editors (the
[Hooks](#hooks) section has the full picture). Re-running it is always
safe, and `jog uninstall` reverses it. Flags answer the questions for
dotfiles, CI, or a coding agent doing the setup for you:

```sh
jog install                                   # guided setup
jog install --yes                             # every default, no questions
jog install --yes --preexec --agents claude   # scoped; see `jog help install`
```

### Verify

Make any change in a repo, run `git status`, then bare `jog` — the
timeline it prints should show a `pre: git status` entry: the alias is
snapshotting. `jog doctor` checks all the wiring and every invariant
the engine depends on.

```console
$ git status             # through the alias — snapshots first
$ jog
no changes since the last snapshot on main

  c0ffee1  deadbee  9 seconds ago  pre: git status
```

## Recovery cookbook

`jog log` browses this branch's timeline. In a terminal it's an
interactive browser — scrub with a live diff preview to find the
version where things still worked, and press `r` to restore right
there. Each snapshot names the commit it was based on (the dim second
column), and `●` rows mark where a commit, rebase, or reset moved
HEAD — the same short ids you see in `git log`, so the two timelines
cross-reference.

```console
$ jog log
c0ffee1  deadbee  2 minutes ago   pre: git status
a1b2c3d  deadbee  9 minutes ago   claude[b3f1a2c4]: Bash(go test ./...)
● deadbee  commit: parser groundwork — 12 minutes ago
9e8d7c6  4f5e6d7  14 minutes ago  manual: before parser rewrite
```

### Other ways of locating a snapshot

`jog since` answers "what did I actually change?" — a per-file summary
since the last command boundary, or any time you give it:

```console
$ jog since 1h           # everything changed since the snapshot nearest 1h ago
since 4d5e6f7 (1 hour ago — pre: git checkout main)
 src/parser.go            | 41 ++++++++---
 src/parser_test.go (new) | 66 ++++++++++++++++++++
 2 files changed, 96 insertions(+), 11 deletions(-)
```

If the work was on another branch or in a worktree, `jog branches`
shows every branch's chain — deleted branches included — and
`jog log --all` interleaves every chain's timeline:

```sh
jog branches
jog log --all
```

### Restoring

Point `jog restore` at what you found: paths for the files to bring
back, and `--at` with a snapshot id, a time, or a position. `--all`
restores the whole tree instead, including deleting files created
since:

```console
$ jog restore src/parser.go --at a1b2c3d
restored src/parser.go from a1b2c3d (9 minutes ago — claude[b3f1a2c4]: Bash(go test ./...))

$ jog restore src/config.yaml --at 9m
restored src/config.yaml from 2c4d6e8 (9 minutes ago — claude[b3f1a2c4]: prompt "clean up the src tree")

$ jog restore --all --at 25m
restored to 9e8d7c6 (26 minutes ago — manual: before parser rewrite): 11 restored, 3 deleted
(undo: jog restore --all)
```

Every restore snapshots first, so undo is itself undoable.

## Usage

### jog

Bare `jog` is a deliberate checkpoint: snapshot now, then show the top
of the timeline. `jog -m "msg"` labels it.

```sh
jog                              # snapshot now, show the top of the timeline
jog -m "before parser rewrite"   # labeled: manual: before parser rewrite
```

### jog log

Browse the timeline of snapshots on this branch. In a terminal it's an
interactive browser — scrub with a live diff preview, press `r` to
restore after a y/n confirm. Piped, it prints plainly: id, the commit
the snapshot was based on, age, provenance, files changed. Rows marked
`●` are commits interleaved where they happened — commit, rebase, and
reset boundaries named with the same ids as `git log`. With paths,
browsing and restoring are scoped to those paths. (`--json` carries
the base commit as `base`; `-p`, `--format`, and `--all` output are
unchanged.)

```sh
jog log                   # this branch's timeline
jog log src/parser.go     # scoped: only snapshots touching these paths
jog log -p -n 5           # the newest 5, with full patches
jog log --all             # every branch's chain, interleaved
jog log --json            # machine-readable
jog log --format='%h %s'  # any git log format
jog snaps                 # snaps and pick are aliases of log
```

### jog branches

One row per branch's snapshot chain — count, newest snapshot, and
whether the branch still exists. Deleted branches' chains stay listed
until their snapshots age out, so you can see what `jog trim --gone`
would drop before it drops it.

```console
$ jog branches
* main      47 snapshots, newest 2m ago — pre: git status
  feature   12 snapshots, newest 3d ago — manual: wip on parser
- old-work   5 snapshots, newest 34d ago — manual: spike
- Deleted branch, clean up with jog trim --gone

$ jog branches --json    # machine-readable; jog branch is an alias
```

### jog since

What changed since a snapshot — per-file summary, `-p` for patches.
With no target it diffs against your last command boundary; give it a
time or position (`jog since 1h`) to widen the window, and paths to
narrow it.

```sh
jog since                 # since the last command boundary
jog since 1h              # since the snapshot nearest an hour ago
jog since -p 30m          # patches instead of the per-file summary
jog since '@{3}' src/     # three snapshots back, scoped to src/
```

### jog restore

Restore files — or with `--all`, the whole tree, including deleting
files created since — from a snapshot. Worktree only: your index,
branches, and HEAD never move.

```sh
jog restore src/parser.go            # one file, from the newest snapshot
jog restore src/parser.go --at 1h    # …as it was an hour ago
jog restore --all --at '@{2}'        # whole tree, two snapshots back
jog restore --all                    # back to the newest snapshot (the undo)
jog back src/parser.go               # back is an alias of restore
```

Every restore snapshots first, so undo is itself undoable.

`--at` (and `since`'s target slot) accepts a snap id from `jog log`, a
time, or a position on the timeline:

- a time: `--at 30m`, `--at 1h`, `--at 2d`, `--at 1w` — plus anything
  git's date syntax accepts (`--at yesterday`, `--at 2.hours.ago`)
- a position: `--at '@{1}'` is one snapshot ago, `--at '@{2}'` two back —
  [git's reflog syntax](https://git-scm.com/docs/gitrevisions), counted
  on the snapshot timeline (keep the quotes; some shells expand braces)

Asking for a time older than the oldest snapshot falls back to the
oldest, with a warning.

### jog git

Pure passthrough: snapshot, then run the real git command exactly as
typed — what the alias expands to. jog matches no verbs and
reimplements nothing, so no git command, user alias, or future git
feature can ever collide with it.

```sh
git checkout -- src/    # typed git goes through the alias = jog git checkout -- src/
jog git stash pop       # …or spell the passthrough out yourself
```

### jog uninstall

Shows everything jog wired — the shell lines, agent hooks and skills,
editor hooks — and removes all of it after one confirmation (`--yes`
skips it). Only jog's own marked lines and managed files are touched:
a hand-written alias, your settings, and any file carrying your edits
are left alone.

```console
$ jog uninstall
jog uninstall — remove jog's wiring

currently wired:
  alias    zsh
  preexec  zsh
  agents   claude
  editors  vim

remove all of it? [y/N] y

removed:
  ✓ zsh        alias   ~/.zshrc
  ✓ zsh        preexec ~/.zshrc
  ✓ claude     hooks   ~/.claude/settings.json
  ✓ claude     skill   ~/.claude/skills/jog/SKILL.md
  ✓ vim        hook    ~/.vim/plugin/jog.vim

snapshots are untouched — they live in each repo's refs/jog/*; `jog trim` prunes them.
the binary itself: rm ~/.local/bin/jog
```

Snapshots are deliberately not part of removal — they're plain git
objects in each repo, and `jog trim` (or deleting `refs/jog/*`) is how
they go. The closing line names the right way to remove the binary for
how it was installed (`rm`, `brew uninstall jog`, …).

## Hooks

jog takes its snapshots from hooks. Three surfaces can carry one — your
shell, your coding agents, and your editor — each wired by its own
`jog <surface> install` command and covered below.

### Shell

Your shell can get two independent lines, each serving a different
purpose: the alias and the preexec hook.

#### The alias

The alias routes every git command you type through `jog git`, which
snapshots the working tree first and then runs real git, exactly as
typed. It only affects your interactive shell — scripts, IDEs, and CI
resolve `git` on PATH and get real git.

```sh
# bash / zsh (~/.bashrc / ~/.zshrc)
alias git='jog git'

# fish (~/.config/fish/config.fish)
alias git 'jog git'
```

```powershell
# PowerShell (add to your profile — `notepad $PROFILE`)
function git { jog git @args }
```

#### The preexec hook

The preexec hook covers the rest of your terminal: it snapshots before
*every* interactive command, so `rm -rf`, `sed -i`, and `make clean`
have a restore point too. Opt in with `jog shell install --preexec`;
it's one line calling `jog shell-hook` from the shell's
before-each-command mechanism:

```sh
# bash (~/.bashrc) — via PS0, bash 4.4+ (older bash ignores it harmlessly)
PS0='$(command -v jog >/dev/null && jog shell-hook --history -- "$(HISTTIMEFORMAT= builtin history 1)")'"$PS0"

# zsh (~/.zshrc)
__jog_preexec() { command -v jog >/dev/null && jog shell-hook -- "$1"; }; preexec_functions+=(__jog_preexec)

# fish (~/.config/fish/config.fish)
function __jog_preexec --on-event fish_preexec; type -q jog; and jog shell-hook -- "$argv"; end
```

(PowerShell has no preexec mechanism — it gets the alias only.)

### Agents

Agents are why jog exists twice over: they're the fastest writers your
worktree has ever had, and the first to delete something you still needed.
jog integrates with every supported client at two surfaces:

- **Hooks** — snapshot before every prompt and every mutating tool call,
  so each agent action has a boundary to roll back to.
- **A skill** — teaches the agent the recovery workflow itself: list
  versions with `jog log`, restore with `jog restore`, checkpoint with
  `jog -m` before risky work, and never declare uncommitted work gone
  without checking jog first.

| client | hooks | skill |
|---|---|---|
| Claude Code | `~/.claude/settings.json` — PreToolUse + UserPromptSubmit | `~/.claude/skills/jog/` |
| Codex | `~/.codex/hooks.json` — needs a one-time trust via `/hooks` | `~/.agents/skills/jog/` |
| Copilot CLI | `~/.copilot/settings.json` | `~/.copilot/skills/jog/` |
| Cursor (IDE + CLI) | `~/.cursor/hooks.json` | `~/.cursor/skills/jog/` |
| Gemini CLI | `~/.gemini/settings.json` | `~/.gemini/skills/jog/` |
| OpenCode | plugin at `~/.config/opencode/plugins/jog.js` | `~/.config/opencode/skills/jog/` |

Install with `jog agents install` and you'll get:

- snapshots the working tree before every prompt and every mutating
  tool call
- introduces jog to the agent once per session — one line saying
  snapshots are live and how to restore (on the clients that support
  injecting context: Claude Code, Codex, Gemini, OpenCode)
- can never block the agent: it always exits 0, and exits in
  milliseconds outside git repos

### Editors

Agents get hooks; so do humans. `jog editors install <editor>` drops a
post-save hook into your editor, so every save inside a git repo becomes a
restorable snapshot — `vim: save src/main.go` in the timeline. This is the
one trigger that snapshots *after* the fact: the saved state is the
checkpoint (the pre-save state is your editor's own undo).

| editor | hook lands in | takes effect |
|---|---|---|
| `vim` | `~/.vim/plugin/jog.vim` | new sessions |
| `nvim` | `~/.config/nvim/plugin/jog.vim` | new sessions |
| `emacs` | `~/.emacs.d/jog.el` | after one `(load …)` line in your init file |
| `sublime` | `Packages/User/jog.py` (per-OS path) | immediately |
| `kakoune` | `~/.config/kak/` (autoload, or sourced from kakrc) | new sessions |
| `micro` | `~/.config/micro/plug/jog/jog.lua` | new sessions |
| `vscode` | `~/.vscode/extensions/` + `~/.vscode-server/extensions/` (whichever exist) | after a full restart (remote windows: reload) |
| `jetbrains` | `.idea/watcherTasks.xml` — per project | after a project reload; needs the File Watchers plugin |

## What jog will never touch

1. Your **index** — byte-identical across any jog operation (jog stages into
   a private shadow index; all reads use `--no-optional-locks`).
2. Your **worktree** — written only by an explicit `jog restore`, which is
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

jog does **not** protect against:

- **GUI discards.** VS Code's git panel, Fork, etc. call git via their own
  bindings — no trigger fires. (VS Code moves discarded files to the trash,
  which is some comfort.)
- **Scripts and CI running real git.** They bypass the alias *by design*.
- **Ignored files.** Never snapshotted, by design — jog is not a backup
  system for `node_modules` or build artifacts.
- **Terminal work without the shell wiring installed.** No trigger, no
  snapshot. The alias only covers git commands; `jog shell install
  --preexec` opts into a hook that snapshots before every interactive
  command (bash ≥ 4.4; PowerShell has no preexec mechanism), but a
  shell it was never wired into is unprotected.

Also worth knowing:

- Preexec labels can go stale; content never does. The bash hook labels
  the snapshot from `history 1`, so with `HISTCONTROL=ignorespace` a
  space-prefixed command gets the previous command's label — the snapshot
  itself is still taken causally before the command runs. Re-sourcing an
  rc file registers the hook twice; the second fire is a no-op.

- `refs/jog/*` stays private through normal `push`/`fetch`/`clone`, but
  **leaks** via `push --mirror`, `clone --mirror`, explicit `refs/*:refs/*`
  refspecs, and `git bundle --all`. Your snapshots contain your scratch work
  — know your mirror scripts. (That includes `refs/jog/@trash/*`, which
  holds snapshots the last `jog trim` dropped, one cycle longer.)
- Retention runs itself: a daily background trim drops snapshots older
  than `keep` (`jog config autoTrim false` makes trim manual-only). Space
  cost is low either way (content-addressed, delta-compressed by repack);
  `jog doctor` shows the actual number, and `maxSize` can cap it.
- New files over 50 MiB are skipped (configurable, see below) and listed in
  the timeline entry.

## Configuration

Native `git config` keys — global or per-repo, no new file format.
`jog config` lists every setting with its current value and meaning;
`jog config <key> <value>` sets one (`--global`, `--unset`), with values
validated through git's own parsers:

| key | default | meaning |
|---|---|---|
| `jog.maxFileSize` | `50m` | skip new files larger than this (`0` disables the guard) |
| `jog.keep` | `90.days` | `jog trim` drops snapshots older than this (`never` keeps everything) |
| `jog.maxSize` | `0` (off) | total disk budget for snapshots — `jog trim` drops oldest first until the estimate fits (one snapshot lenient) |
| `jog.autoTrim` | `1.day` | how often the background trim runs, per repo — git expiry syntax (`12.hours`, `2.weeks`), seconds (`3600`), or a bool (`false` means trim only runs by hand) |
| `jog.updateCheck` | `1.day` | how often the background release check runs — git expiry syntax (`12.hours`, `2.weeks`), seconds (`3600`), or a bool (`false` disables updates and notices entirely, regardless of `autoUpdate`) |
| `jog.autoUpdate` | `true` | install new releases in the background; the next command runs the new version (`false` prints a one-line notice once per release instead; brew and source installs always get the notice) |
| `gc.refs/jog/*.reflogExpire` | `never` | set by jog on first snapshot; keeps gc off jog's reflogs |
| `gc.refs/jog/*.reflogExpireUnreachable` | `never` | same |

Two environment variables:

- `JOG_GIT` — the git binary jog runs, as a name to find on `PATH` or a
  path to the executable. Default: `git` from `PATH`.
- `JOG_DEBUG=1` — hook diagnostics on stderr (hooks are otherwise
  silent by design).

## How it compares

- **[jj]** — the inspiration for snapshot-on-every-command, but it's a new
  VCS with a workflow tax, and it's incompatible with `git worktree`. jog is
  git, plus a memory.
- **[dura](https://github.com/tkellogg/dura)** — background daemon, hash-named
  branches that made recovery painful; dormant.
- **[gitwatch](https://github.com/gitwatch/gitwatch)** — commits your real
  branch on a watcher; pollutes history.
- **Editor local history** (VS Code, JetBrains) — blind to terminal and
  agent changes, short retention, not in git. jog's [editor hooks](#editors)
  put saves on the same timeline as everything else.
- **Claude Code checkpoints** — conversation + Edit/Write only; no bash
  changes, no untracked files, capped at 100 checkpoints / 30 days. jog is
  the other half.

## License

[MIT](LICENSE)
