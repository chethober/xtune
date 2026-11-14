package core

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// App updates: the latest release on GitHub, the file for this device verified against the
// release's SHA256SUMS, and on desktops the files replaced by a copy of this program run as an
// updater once the app has quit (Windows cannot overwrite files in use). Android installs the
// APK through its own installer, which always asks.

var releasesAPI = "https://api.github.com/repos/chethober/xtune/releases/latest"

// UpdateInfo is what the latest release offers this device.
type UpdateInfo struct {
	Current string `json:"current"`
	Latest  string `json:"latest"`
	Newer   bool   `json:"newer"`
	Notes   string `json:"notes,omitempty"`
	Page    string `json:"page,omitempty"`
	Name    string `json:"name,omitempty"` // the file for this device
	URL     string `json:"url,omitempty"`
	Size    int64  `json:"size,omitempty"`
	SHA256  string `json:"sha256,omitempty"`
}

// versionParts reads v1.3.10 (or 1.3.10-beta) as numbers; nil for a development build.
func versionParts(v string) []int {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	var out []int
	for _, p := range strings.Split(v, ".") {
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil
		}
		out = append(out, n)
	}
	return out
}

// newer reports whether a is a later version than b.
func newer(a, b string) bool {
	pa, pb := versionParts(a), versionParts(b)
	if pa == nil || pb == nil {
		return false
	}
	for i := 0; i < max(len(pa), len(pb)); i++ {
		x, y := 0, 0
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if x != y {
			return x > y
		}
	}
	return false
}

// updateGet fetches u directly, or through the running connection when GitHub cannot be reached
// from here (it is filtered on some networks).
func updateGet(u string, timeout time.Duration) (*http.Response, error) {
	try := func(c *http.Client) (*http.Response, error) {
		req, _ := http.NewRequest("GET", u, nil)
		req.Header.Set("User-Agent", "xtune-client/"+Version)
		resp, err := c.Do(req)
		if err == nil && resp.StatusCode != 200 {
			resp.Body.Close()
			return nil, fmt.Errorf("%s: HTTP %d", u, resp.StatusCode)
		}
		return resp, err
	}
	resp, err := try(&http.Client{Transport: directClient.Transport, Timeout: timeout})
	if err == nil {
		return resp, nil
	}
	engine.mu.Lock()
	running, port := engine.inst != nil, engine.opts.SocksPort
	engine.mu.Unlock()
	if !running {
		return nil, err
	}
	pu, _ := url.Parse(fmt.Sprintf("socks5h://127.0.0.1:%d", port))
	if r, err2 := try(&http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(pu)}, Timeout: timeout}); err2 == nil {
		return r, nil
	}
	return nil, err
}

// CheckUpdate asks GitHub for the latest release and picks the file for platform (the end of
// the release's file names: android-arm64-v8a.apk, linux-x64.tar.gz, windows-x64.zip).
func CheckUpdate(platform string) (UpdateInfo, error) {
	info := UpdateInfo{Current: Version}
	resp, err := updateGet(releasesAPI, 20*time.Second)
	if err != nil {
		return info, fmt.Errorf("could not reach GitHub: %w", err)
	}
	defer resp.Body.Close()
	var rel struct {
		Tag    string `json:"tag_name"`
		Body   string `json:"body"`
		Page   string `json:"html_url"`
		Assets []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
			Size int64  `json:"size"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&rel); err != nil {
		return info, fmt.Errorf("unexpected answer from GitHub: %w", err)
	}
	info.Latest, info.Notes, info.Page = rel.Tag, rel.Body, rel.Page
	info.Newer = newer(rel.Tag, Version)
	sums := ""
	for _, a := range rel.Assets {
		switch {
		case a.Name == "SHA256SUMS":
			sums = a.URL
		case strings.HasSuffix(a.Name, "-"+platform):
			info.Name, info.URL, info.Size = a.Name, a.URL, a.Size
		}
	}
	if !info.Newer || info.URL == "" {
		return info, nil
	}
	if sums == "" {
		return info, errors.New("the release has no checksums: update from its page instead")
	}
	r, err := updateGet(sums, 20*time.Second)
	if err != nil {
		return info, err
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	for _, line := range strings.Split(string(b), "\n") {
		if f := strings.Fields(line); len(f) == 2 && strings.TrimPrefix(f[1], "*") == info.Name {
			info.SHA256 = strings.ToLower(f[0])
		}
	}
	if len(info.SHA256) != 64 {
		return info, errors.New("the release's checksums do not list " + info.Name)
	}
	return info, nil
}

// UpdateJob is a download in progress.
type UpdateJob struct {
	State string `json:"state"` // downloading | ready | failed
	Done  int64  `json:"done"`
	Total int64  `json:"total"`
	Path  string `json:"path,omitempty"`
	Error string `json:"error,omitempty"`
	Name  string `json:"name,omitempty"`
}

var (
	updMu  sync.Mutex
	updJob *UpdateJob
)

func updateDir() string {
	dataMu.Lock()
	d := dataDir
	dataMu.Unlock()
	if d == "" {
		d = os.TempDir()
	}
	return filepath.Join(d, "updates")
}

// StartUpdateDownload downloads info's file and checks it against its SHA-256.
func StartUpdateDownload(info UpdateInfo) (UpdateJob, error) {
	if info.URL == "" || len(info.SHA256) != 64 || info.Name == "" || strings.ContainsAny(info.Name, `/\`) {
		return UpdateJob{}, errors.New("nothing to download")
	}
	updMu.Lock()
	defer updMu.Unlock()
	if updJob != nil && updJob.State == "downloading" {
		return *updJob, nil
	}
	dir := updateDir()
	dst := filepath.Join(dir, info.Name)
	if ok, _ := fileHas(dst, info.SHA256); ok { // downloaded before
		updJob = &UpdateJob{State: "ready", Done: info.Size, Total: info.Size, Path: dst, Name: info.Name}
		return *updJob, nil
	}
	j := &UpdateJob{State: "downloading", Total: info.Size, Name: info.Name}
	updJob = j
	go func() {
		err := download(info, dir, dst, j)
		updMu.Lock()
		defer updMu.Unlock()
		if err != nil {
			j.State, j.Error = "failed", err.Error()
		} else {
			j.State, j.Path = "ready", dst
		}
	}()
	return *j, nil
}

func download(info UpdateInfo, dir, dst string, j *UpdateJob) error {
	os.RemoveAll(dir) // older downloads
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	resp, err := updateGet(info.URL, 30*time.Minute)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	part := dst + ".part"
	f, err := os.OpenFile(part, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	h := sha256.New()
	buf := make([]byte, 64<<10)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			f.Write(buf[:n])
			h.Write(buf[:n])
			updMu.Lock()
			j.Done += int64(n)
			updMu.Unlock()
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			f.Close()
			return rerr
		}
	}
	f.Close()
	if got := hex.EncodeToString(h.Sum(nil)); got != info.SHA256 {
		os.Remove(part)
		return fmt.Errorf("the download does not match the release's checksum (%s…): try again", got[:12])
	}
	return os.Rename(part, dst)
}

func fileHas(path, sum string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return false, err
	}
	return hex.EncodeToString(h.Sum(nil)) == sum, nil
}

// UpdateStatus is the download's state; "" when none was started.
func UpdateStatus() UpdateJob {
	updMu.Lock()
	defer updMu.Unlock()
	if updJob == nil {
		return UpdateJob{}
	}
	return *updJob
}

// ApplyUpdate unpacks a release archive (.tar.gz or .zip) over dir. A folder every entry shares
// (xtune/ in the Linux tarball) is dropped. Files in use are moved aside (name.old) first, which
// Windows allows; retries cover the moments the old app and its core take to exit.
func ApplyUpdate(archive, dir string) error {
	type entry struct {
		name string
		mode os.FileMode
		open func() (io.ReadCloser, error)
		dir  bool
	}
	var entries []entry
	switch {
	case strings.HasSuffix(archive, ".zip"):
		z, err := zip.OpenReader(archive)
		if err != nil {
			return err
		}
		defer z.Close()
		for _, f := range z.File {
			f := f
			entries = append(entries, entry{name: f.Name, mode: f.Mode(), dir: f.FileInfo().IsDir(), open: func() (io.ReadCloser, error) { return f.Open() }})
		}
	case strings.HasSuffix(archive, ".tar.gz"):
		f, err := os.Open(archive)
		if err != nil {
			return err
		}
		defer f.Close()
		gz, err := gzip.NewReader(f)
		if err != nil {
			return err
		}
		tr := tar.NewReader(gz)
		for {
			h, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return err
			}
			if h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeDir {
				continue // links are not part of the app's tarball
			}
			b, err := io.ReadAll(io.LimitReader(tr, 512<<20))
			if err != nil {
				return err
			}
			entries = append(entries, entry{name: h.Name, mode: os.FileMode(h.Mode).Perm(), dir: h.Typeflag == tar.TypeDir,
				open: func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader(string(b))), nil }})
		}
	default:
		return errors.New("not a release archive")
	}
	// the folder every entry shares, if any
	prefix := ""
	for i, e := range entries {
		first, _, _ := strings.Cut(strings.TrimPrefix(e.name, "./"), "/")
		if i == 0 {
			prefix = first + "/"
		} else if !strings.HasPrefix(strings.TrimPrefix(e.name, "./"), prefix) {
			prefix = ""
			break
		}
	}
	root, _ := filepath.Abs(dir)
	for _, e := range entries {
		rel := strings.TrimPrefix(strings.TrimPrefix(e.name, "./"), prefix)
		if rel == "" {
			continue
		}
		dst := filepath.Join(root, filepath.FromSlash(rel))
		if dst != root && !strings.HasPrefix(dst, root+string(filepath.Separator)) {
			return fmt.Errorf("unsafe path in the archive: %s", e.name)
		}
		if e.dir {
			if err := os.MkdirAll(dst, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		r, err := e.open()
		if err != nil {
			return err
		}
		tmp := dst + ".new"
		w, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, max(e.mode, 0o644))
		if err == nil {
			_, err = io.Copy(w, r)
			w.Close()
		}
		r.Close()
		if err != nil {
			return err
		}
		var rerr error
		for try := 0; try < 60; try++ {
			if rerr = os.Rename(tmp, dst); rerr == nil {
				break
			}
			// in use (Windows): move it aside, then try again
			os.Remove(dst + ".old")
			os.Rename(dst, dst+".old")
			time.Sleep(500 * time.Millisecond)
		}
		if rerr != nil {
			return fmt.Errorf("could not replace %s: %w", rel, rerr)
		}
	}
	return nil
}

// CleanOldFiles removes what ApplyUpdate moved aside, once the new version runs.
func CleanOldFiles(dir string) {
	filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() && strings.HasSuffix(p, ".old") {
			os.Remove(p)
		}
		return nil
	})
}
