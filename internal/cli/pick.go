package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/tyler-johnson/jog/internal/gitx"
	"github.com/tyler-johnson/jog/internal/provenance"
	"github.com/tyler-johnson/jog/internal/snap"
	"github.com/tyler-johnson/jog/internal/tui"
	"golang.org/x/term"
)

// Pick is `jog pick [--all] <path>`: scrub through a file's versions on the
// timeline — list, preview the change each version introduced, and restore
// the selected one (plan D20: a file scrubber, not a repo browser).
//
// Restoring delegates to the back machinery, so the pre-restore snapshot
// and its undo hint come for free. Without a TTY the version list prints
// plainly instead — pipeable, and the same data the TUI shows.
func Pick(args []string) int {
	all := false
	var paths []string
	for _, a := range args {
		switch a {
		case "--all":
			all = true
		default:
			paths = append(paths, a)
		}
	}
	if len(paths) != 1 {
		fmt.Fprintln(os.Stderr, "jog: usage: jog pick [--all] <path>")
		return 2
	}
	path := paths[0]

	wd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "jog: %v\n", err)
		return 1
	}
	repo, err := gitx.Discover(wd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "jog: %v\n", err)
		return 1
	}

	// A command boundary like the other reads; back's own mandatory
	// snapshot will no-op against it at restore time.
	snap.Take(repo, provenance.Pre(strings.TrimSpace("jog pick "+strings.Join(args, " "))))

	versions, err := fileVersions(repo, path, all)
	if err != nil {
		fmt.Fprintf(os.Stderr, "jog: %v\n", err)
		return 1
	}
	if len(versions) == 0 {
		fmt.Printf("no snapshots touch %s\n", path)
		return 0
	}

	if !term.IsTerminal(int(os.Stdout.Fd())) {
		for _, v := range versions {
			fmt.Printf("%s  %s  %s\n", v.SHA[:7], v.Age, v.Subject)
		}
		return 0
	}

	items := make([]tui.PickItem, len(versions))
	for i, v := range versions {
		items[i] = tui.PickItem{ID: v.SHA, Label: fmt.Sprintf("%s  %s  %s", v.SHA[:7], v.Age, v.Subject)}
	}
	chosen, aborted, err := tui.RunPick(
		fmt.Sprintf("versions of %s — enter restores, q leaves everything untouched", path),
		items,
		func(id string) string { return versionPreview(repo, id, path) },
		"", // single file: enter restores instantly, no confirm step
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "jog: %v\n", err)
		return 1
	}
	if aborted {
		return 0
	}
	return Back([]string{path, "--at", chosen})
}

// pickVersion is one timeline entry that changed the picked file.
type pickVersion struct {
	SHA     string
	Age     string
	Subject string
}

// fileVersions lists the snapshots in which the file changed, newest first
// — the current chain's, or every chain's with all (the same ranges snaps
// uses, so the two views always agree).
func fileVersions(repo *gitx.Repo, path string, all bool) ([]pickVersion, error) {
	var ranges []string
	if all {
		var err error
		if ranges, err = forestRanges(repo); err != nil || ranges == nil {
			return nil, err
		}
	} else {
		_, rng, exists, err := chainRange(repo)
		if err != nil || !exists {
			return nil, err
		}
		ranges = []string{rng}
	}

	gitArgs := append([]string{"log", "--first-parent", "--format=%H\x1f%cr\x1f%s"}, ranges...)
	gitArgs = append(gitArgs, "--", path)
	out, err := repo.RunRead(gitArgs...)
	if err != nil || out == "" {
		return nil, err
	}
	var versions []pickVersion
	for _, line := range strings.Split(out, "\n") {
		f := strings.SplitN(line, "\x1f", 3)
		if len(f) != 3 {
			continue
		}
		versions = append(versions, pickVersion{SHA: f[0], Age: f[1], Subject: f[2]})
	}
	return versions, nil
}

// versionPreview renders what this snapshot changed about the file.
// Snapshots are two-parent commits (previous snapshot + base edge), so a
// plain `git show` would render a combined diff against both — useless
// here. `log --first-parent -p` diffs against parent 1 only: the previous
// snapshot on the chain (and a chain root renders as an addition).
func versionPreview(repo *gitx.Repo, sha, path string) string {
	out, err := repo.RunRead("log", "--first-parent", "-1", "-p", "--format=", "--color=always", sha, "--", path)
	if err != nil || out == "" {
		return "(no preview)"
	}
	return out
}
