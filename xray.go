package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

//go:embed helper.sh
var helperScript string

const remoteDir = "/opt/xray-autotune"

// ---------------------------------------------------------------- remote helper

func (r *Remote) Helper(args ...string) (string, error) {
	if err := r.Upload(remoteDir+"/helper.sh", []byte(helperScript), "700"); err != nil {
		return "", fmt.Errorf("upload helper: %w", err)
	}
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = shq(a)
	}
	out, err := r.Root("bash " + remoteDir + "/helper.sh " + strings.Join(q, " "))
	return strings.TrimSpace(out), err
}

type ServerStatus struct {
	Arch, Xray, Unit           string // isolated xtune Xray
	HasState                   bool   // a standalone deployment exists
	XUI                        bool   // 3x-ui installed
	XUIPort, XUIBase           string
	XUIToken, XUIXray, XUIUnit string
}

func (r *Remote) Status() (ServerStatus, error) {
	out, err := r.Helper("status")
	var s ServerStatus
	for _, line := range strings.Split(out, "\n") {
		k, v, _ := strings.Cut(strings.TrimSpace(line), " ")
		switch k {
		case "ARCH":
			s.Arch = v
		case "XRAY":
			s.Xray = v
		case "UNIT":
			s.Unit = v
		case "STATE":
			s.HasState = v == "yes"
		case "XUI":
			s.XUI = v == "yes"
		case "XUI_PORT":
			s.XUIPort = v
		case "XUI_BASE":
			s.XUIBase = v
		case "XUI_TOKEN":
			s.XUIToken = v
		case "XUI_XRAY":
			s.XUIXray = v
		case "XUI_UNIT":
			s.XUIUnit = v
		}
	}
	return s, err
}

// ---------------------------------------------------------------- versions

var verRe = regexp.MustCompile(`v?(\d+)\.(\d+)\.(\d+)`)

// newer reports whether version a is newer than b ("v26.9.30" style; "" / "none" is oldest).
func newer(a, b string) bool {
	pa, pb := verRe.FindStringSubmatch(a), verRe.FindStringSubmatch(b)
	if pa == nil {
		return false
	}
	if pb == nil {
		return true
	}
	for i := 1; i <= 3; i++ {
		x, _ := strconv.Atoi(pa[i])
		y, _ := strconv.Atoi(pb[i])
		if x != y {
			return x > y
		}
	}
	return false
}

func normTag(v string) string {
	if m := verRe.FindString(v); m != "" {
		return "v" + strings.TrimPrefix(m, "v")
	}
	return v
}

// httpGet tries a direct connection first (the user's proxy may be the thing being tuned),
// then whatever proxy the environment configures.
func httpGet(url string, timeout time.Duration) ([]byte, error) {
	var lastErr error
	for _, tr := range []*http.Transport{{Proxy: nil}, {Proxy: http.ProxyFromEnvironment}} {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
		req.Header.Set("User-Agent", "xtune/"+version)
		resp, err := (&http.Client{Transport: tr}).Do(req)
		if err == nil {
			body, rerr := io.ReadAll(resp.Body)
			resp.Body.Close()
			cancel()
			if rerr == nil && resp.StatusCode == 200 {
				return body, nil
			}
			lastErr = fmt.Errorf("GET %s: HTTP %d %v", url, resp.StatusCode, rerr)
			continue
		}
		cancel()
		lastErr = err
	}
	return nil, lastErr
}

// LatestXray returns the newest Xray tag. Pre-releases count unless stable is set, because
// Xray ships most releases as pre-releases. Falls back to asking the server.
func LatestXray(stable bool, r *Remote) (string, error) {
	url := "https://api.github.com/repos/XTLS/Xray-core/releases?per_page=5"
	if stable {
		url = "https://api.github.com/repos/XTLS/Xray-core/releases/latest"
	}
	if body, err := httpGet(url, 20*time.Second); err == nil {
		var one struct {
			TagName string `json:"tag_name"`
		}
		var many []struct {
			TagName string `json:"tag_name"`
		}
		if stable && json.Unmarshal(body, &one) == nil && one.TagName != "" {
			return one.TagName, nil
		}
		if !stable && json.Unmarshal(body, &many) == nil && len(many) > 0 {
			return many[0].TagName, nil
		}
	}
	if r != nil {
		arg := "pre"
		if stable {
			arg = "stable"
		}
		if tag, err := r.Helper("latest", arg); err == nil && tag != "" {
			return tag, nil
		}
	}
	return "", fmt.Errorf("could not determine the latest Xray release (GitHub unreachable)")
}

// ---------------------------------------------------------------- local binary

func dataDir() string {
	d, err := os.UserConfigDir()
	if err != nil {
		d = "."
	}
	return filepath.Join(d, "xtune")
}

func exeName() string {
	if runtime.GOOS == "windows" {
		return "xray.exe"
	}
	return "xray"
}

func managedXray() string { return filepath.Join(dataDir(), "bin", exeName()) }

// FindLocalXray returns the binary to use: --xray, then xtune's managed copy, then PATH,
// then common client install locations.
func FindLocalXray(flagPath string) string {
	home, _ := os.UserHomeDir()
	cands := []string{flagPath, managedXray()}
	if p, err := exec.LookPath("xray"); err == nil {
		cands = append(cands, p)
	}
	cands = append(cands,
		filepath.Join(home, ".local/share/v2rayN/bin/xray", exeName()),
		filepath.Join(home, "Library/Application Support/v2rayN/bin/xray", exeName()),
		"/usr/local/bin/xray", "/opt/homebrew/bin/xray")
	for _, c := range cands {
		if c == "" {
			continue
		}
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c
		}
	}
	return ""
}

func XrayVersion(bin string) string {
	if bin == "" {
		return "none"
	}
	out, err := exec.Command(bin, "version").Output()
	if err != nil {
		return "broken"
	}
	f := strings.Fields(string(out))
	if len(f) >= 2 {
		return f[1]
	}
	return "unknown"
}

func assetName() (string, error) {
	m := map[string]string{
		"linux/amd64": "linux-64", "linux/arm64": "linux-arm64-v8a", "linux/386": "linux-32", "linux/arm": "linux-arm32-v7a",
		"darwin/amd64": "macos-64", "darwin/arm64": "macos-arm64-v8a",
		"windows/amd64": "windows-64", "windows/arm64": "windows-arm64-v8a", "windows/386": "windows-32",
		"freebsd/amd64": "freebsd-64", "freebsd/arm64": "freebsd-arm64-v8a",
	}
	a, ok := m[runtime.GOOS+"/"+runtime.GOARCH]
	if !ok {
		return "", fmt.Errorf("no Xray release for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	return "Xray-" + a + ".zip", nil
}

// InstallLocalXray downloads tag into xtune's own bin dir (never touching other installs),
// verifying the release's SHA2-256 digest.
func InstallLocalXray(tag string, log func(string)) (string, error) {
	asset, err := assetName()
	if err != nil {
		return "", err
	}
	base := "https://github.com/XTLS/Xray-core/releases/download/" + tag + "/" + asset
	log("downloading " + base)
	zipData, err := httpGet(base, 5*time.Minute)
	if err != nil {
		return "", err
	}
	dgst, err := httpGet(base+".dgst", time.Minute)
	if err != nil {
		return "", err
	}
	want := regexp.MustCompile(`SHA2-256=\s*([0-9a-f]{64})`).FindSubmatch(dgst)
	sum := sha256.Sum256(zipData)
	if want == nil || string(want[1]) != hex.EncodeToString(sum[:]) {
		return "", fmt.Errorf("checksum mismatch for %s", asset)
	}
	zr, err := zip.NewReader(bytes.NewReader(zipData), int64(len(zipData)))
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(managedXray())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	for _, f := range zr.File {
		name := filepath.Base(f.Name)
		if name != exeName() && !strings.HasSuffix(name, ".dat") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return "", err
		}
		tmp := filepath.Join(dir, name+".new")
		w, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			rc.Close()
			return "", err
		}
		_, cerr := io.Copy(w, rc)
		rc.Close()
		w.Close()
		if cerr != nil {
			return "", cerr
		}
		if err := os.Rename(tmp, filepath.Join(dir, name)); err != nil {
			return "", err
		}
	}
	return managedXray(), nil
}

// EnsureLocalXray returns a usable local binary, downloading one if none exists.
func EnsureLocalXray(flagPath string, r *Remote, log func(string)) (string, error) {
	if b := FindLocalXray(flagPath); b != "" && XrayVersion(b) != "broken" {
		return b, nil
	}
	tag, err := LatestXray(false, r)
	if err != nil {
		return "", err
	}
	return InstallLocalXray(tag, log)
}

// EnsureServerXray installs the isolated server binary when missing (or a specific tag).
func EnsureServerXray(r *Remote, tag string, log func(string)) (string, error) {
	st, err := r.Status()
	if err != nil {
		return "", err
	}
	if tag == "" && st.Xray != "none" && st.Xray != "" {
		return st.Xray, nil
	}
	if tag == "" {
		if tag, err = LatestXray(false, r); err != nil {
			return "", err
		}
	}
	log("installing Xray " + tag + " on the server (" + remoteDir + ")")
	out, err := r.Helper("install", normTag(tag))
	return out, err
}
