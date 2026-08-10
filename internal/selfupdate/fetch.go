package selfupdate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
)

type release struct {
	TagName string  `json:"tag_name"`
	Assets  []asset `json:"assets"`
}

type asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

// latest asks the GitHub API for the newest release. GITHUB_TOKEN, when
// present, authenticates the request past the anonymous rate limit.
func (u *Updater) latest() (*release, error) {
	req, err := http.NewRequest("GET", u.APIBase+"/repos/tyler-johnson/jog/releases/latest", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if tok := os.Getenv("GITHUB_TOKEN"); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := u.do(req)
	if err != nil {
		return nil, fmt.Errorf("checking for the latest release: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden && resp.Header.Get("X-RateLimit-Remaining") == "0" {
		return nil, fmt.Errorf("GitHub API rate limit hit — try again later, or set GITHUB_TOKEN")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("checking for the latest release: GitHub answered %s", resp.Status)
	}
	var rel release
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, fmt.Errorf("reading the release listing: %w", err)
	}
	return &rel, nil
}

// fetchAll downloads a small asset (checksums.txt) whole.
func (u *Updater) fetchAll(url string) ([]byte, error) {
	body, err := u.get(url)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	return io.ReadAll(body)
}

// download streams url into a temp file in dir, hashing as it writes.
// The temp file is the caller's to remove.
func (u *Updater) download(url, dir string) (path, sha string, err error) {
	f, err := os.CreateTemp(dir, ".jog-update-*")
	if err != nil {
		return "", "", err
	}
	defer func() {
		f.Close()
		if err != nil {
			os.Remove(f.Name())
		}
	}()
	body, err := u.get(url)
	if err != nil {
		return "", "", err
	}
	defer body.Close()
	h := sha256.New()
	if _, err = io.Copy(io.MultiWriter(f, h), body); err != nil {
		return "", "", fmt.Errorf("downloading %s: %w", url, err)
	}
	return f.Name(), hex.EncodeToString(h.Sum(nil)), nil
}

func (u *Updater) get(url string) (io.ReadCloser, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := u.do(req)
	if err != nil {
		return nil, fmt.Errorf("downloading %s: %w", url, err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("downloading %s: GitHub answered %s", url, resp.Status)
	}
	return resp.Body, nil
}

func (u *Updater) do(req *http.Request) (*http.Response, error) {
	// GitHub rejects requests without a User-Agent.
	req.Header.Set("User-Agent", "jog")
	return u.Client.Do(req)
}
