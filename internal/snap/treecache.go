// The tree cache: $GIT_DIR/jog/trees.json maps commit sha → tree sha for
// the handful of commits the next Take will ask about (the chain head and
// HEAD). A sha names its tree forever, so entries can never go stale —
// the only failure modes are absence and corruption, and both merely cost
// the rev-parse spawn the cache exists to save. The file is advisory,
// rewritten whole on every save (so it self-prunes), and written
// atomically (temp + rename) so a concurrent hook reads a complete
// document or none.
package snap

import (
	"encoding/json"
	"os"
	"path/filepath"
)

func treeCachePath(gitDir string) string {
	return filepath.Join(gitDir, "jog", "trees.json")
}

// loadTrees reads the cache; any doubt — unreadable, unparsable, a value
// that is not a sha — returns nil, and the caller falls back to git.
func loadTrees(gitDir string) map[string]string {
	b, err := os.ReadFile(treeCachePath(gitDir))
	if err != nil {
		return nil
	}
	var m map[string]string
	if json.Unmarshal(b, &m) != nil {
		return nil
	}
	for k, v := range m {
		if !isSHA(k) || !isSHA(v) {
			return nil // corrupt: trust none of it
		}
	}
	return m
}

// saveTrees replaces the cache with m. Best-effort: a failure costs one
// future spawn, never a snapshot.
func saveTrees(gitDir string, m map[string]string) {
	if len(m) == 0 {
		return
	}
	dir := filepath.Join(gitDir, "jog")
	if os.MkdirAll(dir, 0o755) != nil {
		return
	}
	b, err := json.Marshal(m)
	if err != nil {
		return
	}
	f, err := os.CreateTemp(dir, "trees-*.json")
	if err != nil {
		return
	}
	tmp := f.Name()
	_, werr := f.Write(b)
	cerr := f.Close()
	if werr != nil || cerr != nil || os.Rename(tmp, treeCachePath(gitDir)) != nil {
		os.Remove(tmp)
	}
}

// isSHA reports a plausible object id: full-length lowercase hex, sha1 or
// sha256 sized.
func isSHA(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
