package editors

import (
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/tyler-johnson/jog/internal/gitx"
	"github.com/tyler-johnson/jog/internal/install"
)

// JetBrains IDEs run save hooks through the bundled File Watchers plugin,
// whose config is .idea/watcherTasks.xml — inherently per project, so
// this is the one editor users re-run per repo. The task invokes bare
// `jog` deliberately: .idea is routinely committed, and a machine's
// absolute path has no business in version control.
//
// The merge philosophy mirrors the agents' JSON wiring: parse, add or
// remove exactly jog's task, write back with every foreign task carried
// verbatim (xml:",innerxml"), and hard-error on anything unexpected
// rather than rewrite a file that can't be read back faithfully.

// jetbrainsMarker identifies jog's watcher task, however the user
// reshaped it.
const jetbrainsMarker = "editor-hook jetbrains"

// jetbrainsTask is jog's TaskOptions body. immediateSync=false fires the
// watcher on explicit save and frame deactivation, not every keystroke;
// runOnExternalChanges=false keeps VCS operations from double-firing
// (jog's other triggers cover that world); exitCodeBehavior NEVER and
// checkSyntaxErrors=false make sure not even a broken save is skipped.
const jetbrainsTask = `
      <option name="arguments" value="editor-hook jetbrains $FilePath$" />
      <option name="checkSyntaxErrors" value="false" />
      <option name="description" value="jog — snapshots the repo on save (installed by jog editors install)" />
      <option name="exitCodeBehavior" value="NEVER" />
      <option name="fileExtension" value="*" />
      <option name="immediateSync" value="false" />
      <option name="name" value="jog" />
      <option name="output" value="" />
      <option name="outputFilters">
        <array />
      </option>
      <option name="outputFromStdout" value="false" />
      <option name="program" value="jog" />
      <option name="runOnExternalChanges" value="false" />
      <option name="scopeName" value="Project Files" />
      <option name="trackOnlyRoot" value="false" />
      <option name="workingDir" value="$ProjectFileDir$" />
    `

type watcherFile struct {
	XMLName   xml.Name         `xml:"project"`
	Version   string           `xml:"version,attr"`
	Component watcherComponent `xml:"component"`
}

type watcherComponent struct {
	Name  string        `xml:"name,attr"`
	Tasks []watcherTask `xml:"TaskOptions"`
}

type watcherTask struct {
	IsEnabled string `xml:"isEnabled,attr"`
	Inner     string `xml:",innerxml"`
}

// jetbrainsPath resolves the current project's watcherTasks.xml — or
// explains what is missing. The hook can only live in a project, so
// unlike every other editor this never falls back to the home directory,
// and jog won't invent .idea where no IDE has made one.
func jetbrainsPath() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	repo, err := gitx.Discover(wd)
	if err != nil || repo.Top == "" {
		return "", fmt.Errorf("the hook lives in the project's .idea directory — run this inside a git repository")
	}
	idea := filepath.Join(repo.Top, ".idea")
	if !install.FileExists(idea) {
		return "", fmt.Errorf("no .idea directory here — open the project in a JetBrains IDE once, then re-run this")
	}
	return filepath.Join(idea, "watcherTasks.xml"), nil
}

func jetbrainsLoad(path string) (*watcherFile, error) {
	w := &watcherFile{Version: "4", Component: watcherComponent{Name: "ProjectTasksOptions"}}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return w, nil
	}
	if err != nil {
		return nil, err
	}
	if err := xml.Unmarshal(b, w); err != nil {
		return nil, fmt.Errorf("%s is not valid XML (%v) — fix it, or add the watcher by hand", path, err)
	}
	if w.Component.Name != "ProjectTasksOptions" {
		return nil, fmt.Errorf("%s has an unexpected shape — add the watcher by hand", path)
	}
	return w, nil
}

func jetbrainsWrite(path string, w *watcherFile) error {
	b, err := xml.MarshalIndent(w, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append([]byte(xml.Header), append(b, '\n')...), 0o644)
}

func jetbrainsInstall() (string, bool, error) {
	path, err := jetbrainsPath()
	if err != nil {
		return "", false, err
	}
	w, err := jetbrainsLoad(path)
	if err != nil {
		return "", false, err
	}
	for _, t := range w.Component.Tasks {
		if strings.Contains(t.Inner, jetbrainsMarker) {
			return "already wired in " + path, false, nil
		}
	}
	w.Component.Tasks = append(w.Component.Tasks, watcherTask{IsEnabled: "true", Inner: jetbrainsTask})
	if err := jetbrainsWrite(path, w); err != nil {
		return "", false, err
	}
	return "wired a File Watcher in " + path + " (program: jog)", true, nil
}

func jetbrainsUninstall() (string, bool, error) {
	path, err := jetbrainsPath()
	if err != nil {
		return "", false, err
	}
	if !install.FileExists(path) {
		return "no watcher file at " + path + " — nothing to remove", false, nil
	}
	w, err := jetbrainsLoad(path)
	if err != nil {
		return "", false, err
	}
	kept := w.Component.Tasks[:0]
	removed := 0
	for _, t := range w.Component.Tasks {
		if strings.Contains(t.Inner, jetbrainsMarker) {
			removed++
			continue
		}
		kept = append(kept, t)
	}
	if removed == 0 {
		return "no jog watcher in " + path + " — nothing to remove", false, nil
	}
	w.Component.Tasks = kept
	if len(kept) == 0 {
		if err := os.Remove(path); err != nil {
			return "", false, err
		}
		return "removed the jog watcher from " + path + " (file deleted — nothing else was in it)", true, nil
	}
	if err := jetbrainsWrite(path, w); err != nil {
		return "", false, err
	}
	return "removed the jog watcher from " + path + " — everything else untouched", true, nil
}

func jetbrainsLocation() string {
	path, err := jetbrainsPath()
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(b), jetbrainsMarker) {
		return ""
	}
	return install.ProjectDisplay(path)
}

var jetbrainsEditor = editor{
	name:  "jetbrains",
	title: "JetBrains IDEs",
	detect: func() bool {
		switch runtime.GOOS {
		case "darwin":
			return exists(install.HomePath("Library", "Application Support", "JetBrains"))
		case "windows":
			return exists(roamingAppData("JetBrains"))
		}
		return exists(xdgConfig("JetBrains")) || exists(install.HomePath(".local", "share", "JetBrains"))
	},
	hookInstall:   jetbrainsInstall,
	hookUninstall: jetbrainsUninstall,
	location:      jetbrainsLocation,
	notes: func() []string {
		return []string{
			"needs the File Watchers plugin — bundled in the paid IDEs, installed manually in the Community editions",
			"reload the project (or restart the IDE) so it reads .idea/watcherTasks.xml",
			".idea is often committed to git — check git status before your next commit",
			"per-project by nature: re-run this in each project you want covered",
		}
	},
}
