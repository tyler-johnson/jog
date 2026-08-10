package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func editorHook(t *testing.T, editor string, args []string) {
	t.Helper()
	if code := EditorHook(editor, args); code != 0 {
		t.Fatalf("EditorHook(%q, %v) exited %d — the iron rule is exit 0, always", editor, args, code)
	}
}

func TestEditorHookSubject(t *testing.T) {
	tr := setup(t)
	tr.Write("src/main.go", "package main\n")
	editorHook(t, "vim", []string{filepath.Join(tr.Dir, "src", "main.go")})
	if got := subject(tr); got != "vim: save src/main.go" {
		t.Errorf("subject = %q", got)
	}
}

func TestEditorHookDiscoversFromFileDir(t *testing.T) {
	tr := setup(t)
	tr.Write("b.txt", "y\n")
	// The editor's cwd is somewhere unrelated; only the file's own
	// directory says which repo to snapshot.
	t.Chdir(t.TempDir())
	editorHook(t, "nvim", []string{filepath.Join(tr.Dir, "b.txt")})
	if got := subject(tr); got != "nvim: save b.txt" {
		t.Errorf("subject = %q", got)
	}
}

func TestEditorHookRelativePath(t *testing.T) {
	tr := setup(t)
	tr.Write("src/deep.go", "package deep\n")
	t.Chdir(tr.Dir)
	editorHook(t, "vim", []string{filepath.Join("src", "deep.go")})
	if got := subject(tr); got != "vim: save src/deep.go" {
		t.Errorf("subject = %q", got)
	}
}

func TestEditorHookSpacesInPath(t *testing.T) {
	tr := setup(t)
	tr.Write("my file.txt", "spaces\n")
	// An unquoted editor hook splits the path into several argv entries;
	// rejoining is the forgiving contract.
	editorHook(t, "micro", []string{filepath.Join(tr.Dir, "my"), "file.txt"})
	if got := subject(tr); got != "micro: save my file.txt" {
		t.Errorf("subject = %q", got)
	}
}

func TestEditorHookNoFile(t *testing.T) {
	tr := setup(t)
	tr.Write("c.txt", "z\n")
	t.Chdir(tr.Dir)
	editorHook(t, "vim", nil)
	if got := subject(tr); got != "vim: save" {
		t.Errorf("subject = %q", got)
	}
}

func TestEditorHookOutsideRepoSilent(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	dir := t.TempDir()
	file := filepath.Join(dir, "loose.txt")
	if err := os.WriteFile(file, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	editorHook(t, "vim", []string{file}) // exit 0, nothing to assert but the code
}
