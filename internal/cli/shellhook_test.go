package cli

import "testing"

// stripHistoryIndex must remove exactly bash's `history 1` prefix — the
// entry number, an optional `*` (the modified-entry marker), and the
// surrounding spaces — and nothing from the command itself.
func TestStripHistoryIndex(t *testing.T) {
	cases := []struct{ in, want string }{
		{"  42  rm -rf build", "rm -rf build"},
		{"  42* rm -rf build", "rm -rf build"},
		{"1  git status", "git status"},
		{"7\tmake clean", "make clean"},
		{"", ""},
		{"rm -rf build", "rm -rf build"},       // no index — left alone
		{"7z x archive.7z", "7z x archive.7z"}, // digits glued to the command are not an index
		{"  100  echo 42", "echo 42"},          // only the leading index goes
	}
	for _, c := range cases {
		if got := stripHistoryIndex(c.in); got != c.want {
			t.Errorf("stripHistoryIndex(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
