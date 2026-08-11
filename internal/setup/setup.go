// Package setup implements `jog install` and `jog uninstall` — the
// guided walk through jog's three wiring surfaces: the shell alias,
// agent hooks, and editor save hooks.
//
// It owns no mechanics: every yes routes to the same package the
// standalone command uses (`jog shell`, `jog agents`, `jog editors`),
// so output, idempotence, and edge handling are identical whichever
// door the user came in. Questions are plain stdin lines — no TTY
// required, so piped answers and `--yes` (take every default) work in
// scripts. EOF stops the remaining questions; whatever was already done
// stays, because every step is additive and idempotent.
package setup

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tyler-johnson/jog/internal/agents"
	"github.com/tyler-johnson/jog/internal/editors"
	"github.com/tyler-johnson/jog/internal/install"
	"github.com/tyler-johnson/jog/internal/selfupdate"
	"github.com/tyler-johnson/jog/internal/shell"
)

const installUsage = "jog: usage: jog install [--yes]"
const uninstallUsage = "jog: usage: jog uninstall [--yes]"

// asker answers questions: interactively from stdin, or with the
// default under --yes. ok=false means stdin closed — stop asking.
type asker func(prompt string, def bool) (answer, ok bool)

func newAsker(yes bool) asker {
	if yes {
		return func(_ string, def bool) (bool, bool) { return def, true }
	}
	r := bufio.NewReader(os.Stdin)
	return func(prompt string, def bool) (bool, bool) { return readAnswer(r, prompt, def) }
}

func parseYes(args []string, usage string) (yes, ok bool) {
	for _, a := range args {
		switch a {
		case "--yes", "-y":
			yes = true
		default:
			fmt.Fprintln(os.Stderr, usage)
			return false, false
		}
	}
	return yes, true
}

// Install is `jog install`: one question per surface, then the same
// installers the standalone commands run.
func Install(args []string) int {
	yes, ok := parseYes(args, installUsage)
	if !ok {
		return 2
	}
	ask := newAsker(yes)
	code := 0
	step := func(c int) {
		if c > code {
			code = c
		}
	}
	note := func(n string) { fmt.Println(install.StyleDim.Render("· " + n)) }

	fmt.Println(install.StyleTitle.Render("jog install") + install.StyleDim.Render(" — the alias, agent hooks, and editor hooks, one question each"))
	fmt.Println()

	// The alias: the login shell is the default question; other shells
	// with an rc file on disk are offered as follow-ups.
	login, haveLogin := shell.Login()
	aborted := false
	if !haveLogin {
		note("cannot tell your shell from $SHELL — `jog shell install <name>` wires the alias by hand")
	}
	for _, st := range shell.Statuses() {
		if aborted || (st.Name != login && !st.RCExists) || st.Installed || st.ByHand {
			if st.Name == login && (st.Installed || st.ByHand) {
				note(st.Name + " already has the alias (" + st.RC + ")")
			}
			continue
		}
		prompt := "add `" + st.Line + "` to " + st.RC + "?"
		def := true
		if st.Name != login {
			prompt = "also found " + st.RC + " — add the alias there too?"
			def = false
		}
		answer, ok := ask(prompt, def)
		if !ok {
			aborted = true
			break
		}
		if answer {
			fmt.Println()
			step(shell.Run([]string{"install", st.Name}))
			fmt.Println()
		}
	}

	// Agents: one question covers every detected client — `jog agents
	// install` already fans out to exactly those.
	if !aborted {
		var detected []string
		for _, st := range agents.Statuses() {
			if st.Detected {
				detected = append(detected, st.Name)
			}
		}
		if len(detected) == 0 {
			note("no agent clients detected — `jog agents list` shows the supported ones")
		} else if answer, ok := ask("install hooks + skill for detected agents ("+strings.Join(detected, ", ")+")?", true); !ok {
			aborted = true
		} else if answer {
			fmt.Println()
			step(agents.Run([]string{"install"}))
			fmt.Println()
		}
	}

	// Editors: one question per detected editor — install is deliberately
	// one-at-a-time so each editor's caveats get read.
	if !aborted {
		any := false
		for _, st := range editors.Statuses() {
			if !st.Detected || st.Location != "" {
				continue
			}
			any = true
			answer, ok := ask(st.Name+" detected — install its save hook?", true)
			if !ok {
				aborted = true
				break
			}
			if answer {
				fmt.Println()
				step(editors.Run([]string{"install", st.Name}))
				fmt.Println()
			}
		}
		if !any && !aborted {
			note("no editors detected — `jog editors list` shows the supported ones")
		}
	}

	fmt.Println()
	if aborted {
		note("stopped early — everything already done stays; re-run `jog install` to continue")
	}
	fmt.Println("`jog doctor` verifies the wiring; a `git status` in any repo takes the first snapshot.")
	return code
}

// Uninstall is `jog uninstall`: show what is wired, confirm once, then
// run each surface's own uninstaller.
func Uninstall(args []string) int {
	yes, ok := parseYes(args, uninstallUsage)
	if !ok {
		return 2
	}

	var shells, agentNames, editorNames []string
	for _, st := range shell.Statuses() {
		if st.Installed {
			shells = append(shells, st.Name)
		}
	}
	for _, st := range agents.Statuses() {
		if st.HooksLocation != "" || st.SkillLocation != "" {
			agentNames = append(agentNames, st.Name)
		}
	}
	for _, st := range editors.Statuses() {
		if st.Location != "" {
			editorNames = append(editorNames, st.Name)
		}
	}

	fmt.Println(install.StyleTitle.Render("jog uninstall") + install.StyleDim.Render(" — remove jog's wiring"))
	fmt.Println()
	if len(shells)+len(agentNames)+len(editorNames) == 0 {
		fmt.Println("nothing is wired — no alias, agent hooks, or editor hooks found.")
		binaryHint()
		return 0
	}
	row := func(kind string, names []string) {
		if len(names) > 0 {
			fmt.Printf("  %-8s %s\n", kind, strings.Join(names, ", "))
		}
	}
	fmt.Println("currently wired:")
	row("alias", shells)
	row("agents", agentNames)
	row("editors", editorNames)
	fmt.Println()

	if !yes {
		confirmed, ok := readAnswer(bufio.NewReader(os.Stdin), "remove all of it?", false)
		if !ok || !confirmed {
			fmt.Println(install.StyleDim.Render("nothing removed"))
			return 0
		}
		fmt.Println()
	}

	code := 0
	step := func(c int) {
		if c > code {
			code = c
		}
	}
	if len(shells) > 0 {
		step(shell.Run(append([]string{"uninstall"}, shells...)))
		fmt.Println()
	}
	if len(agentNames) > 0 {
		step(agents.Run(append([]string{"uninstall"}, agentNames...)))
		fmt.Println()
	}
	for _, name := range editorNames {
		step(editors.Run([]string{"uninstall", name}))
		fmt.Println()
	}

	fmt.Println("snapshots are untouched — they live in each repo's refs/jog/*; `jog trim` prunes them.")
	binaryHint()
	return code
}

// binaryHint names the channel-appropriate way to remove the binary
// itself — the one thing uninstall deliberately does not do.
func binaryHint() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	if selfupdate.IsBrewInstall(exe) {
		fmt.Println(install.StyleDim.Render("the binary itself: `brew uninstall jog`"))
		return
	}
	fmt.Println(install.StyleDim.Render("the binary itself: rm " + install.TildePath(exe)))
}
