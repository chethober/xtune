package core

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestVersions(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{{"v1.3.1", "v1.3.0", true}, {"v1.3.0", "v1.3.0", false}, {"v1.10.0", "v1.9.9", true}, {"v1.3", "v1.3.0", false},
		{"v1.4.0", "dev", false}, {"v2.0.0", "1.9.0-beta", true}, {"v1.2.9", "v1.3.0", false}} {
		if got := newer(c.a, c.b); got != c.want {
			t.Errorf("newer(%s, %s) = %v", c.a, c.b, got)
		}
	}
}

// A fake GitHub: the latest release with an APK, a tarball and SHA256SUMS.
func fakeReleases(t *testing.T, apk []byte, sumOverride string) *httptest.Server {
	var srv *httptest.Server
	sum := sha256.Sum256(apk)
	sums := fmt.Sprintf("%s  xtune-v9.0.0-android-arm64-v8a.apk\n%s  xtune-v9.0.0-linux-x64.tar.gz\n", hex.EncodeToString(sum[:]), strings.Repeat("0", 64))
	if sumOverride != "" {
		sums = sumOverride
	}
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest":
			fmt.Fprintf(w, `{"tag_name":"v9.0.0","body":"notes","html_url":"https://example/rel","assets":[
				{"name":"xtune-v9.0.0-android-arm64-v8a.apk","browser_download_url":"%[1]s/apk","size":%[2]d},
				{"name":"xtune-v9.0.0-linux-x64.tar.gz","browser_download_url":"%[1]s/tgz","size":10},
				{"name":"SHA256SUMS","browser_download_url":"%[1]s/sums","size":100}]}`, srv.URL, len(apk))
		case "/apk":
			w.Write(apk)
		case "/sums":
			io.WriteString(w, sums)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestCheckAndDownloadUpdate(t *testing.T) {
	defer func(a, v string) { releasesAPI, Version = a, v }(releasesAPI, Version)
	dataMu.Lock()
	old := dataDir
	dataMu.Unlock()
	defer SetDataDir(old) // the temporary folder goes away with this test; later tests log to the old one
	SetDataDir(t.TempDir())
	apk := bytes.Repeat([]byte("apk!"), 50000)
	srv := fakeReleases(t, apk, "")
	releasesAPI, Version = srv.URL+"/latest", "v1.3.1"
	info, err := CheckUpdate("android-arm64-v8a.apk")
	if err != nil {
		t.Fatal(err)
	}
	if !info.Newer || info.Latest != "v9.0.0" || info.Name != "xtune-v9.0.0-android-arm64-v8a.apk" || len(info.SHA256) != 64 {
		t.Fatalf("info: %+v", info)
	}
	if _, err := StartUpdateDownload(info); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	j := UpdateStatus()
	for j.State == "downloading" && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
		j = UpdateStatus()
	}
	if j.State != "ready" || j.Done != int64(len(apk)) {
		t.Fatalf("download: %+v", j)
	}
	if b, _ := os.ReadFile(j.Path); !bytes.Equal(b, apk) {
		t.Error("the file is the release's")
	}
	// the same release again: already there
	if j2, _ := StartUpdateDownload(info); j2.State != "ready" {
		t.Errorf("again: %+v", j2)
	}
	// not newer: nothing to do
	Version = "v9.0.0"
	if info, _ := CheckUpdate("android-arm64-v8a.apk"); info.Newer {
		t.Error("the same version is not an update")
	}
	// a file that does not match its checksum is refused
	Version = "v1.3.1"
	bad := fakeReleases(t, apk, strings.Repeat("ab", 32)+"  xtune-v9.0.0-android-arm64-v8a.apk\n")
	releasesAPI = bad.URL + "/latest"
	info, err = CheckUpdate("android-arm64-v8a.apk")
	if err != nil {
		t.Fatal(err)
	}
	updMu.Lock()
	updJob = nil
	updMu.Unlock()
	StartUpdateDownload(info)
	for j = UpdateStatus(); j.State == "downloading"; j = UpdateStatus() {
		time.Sleep(20 * time.Millisecond)
	}
	if j.State != "failed" || !strings.Contains(j.Error, "checksum") {
		t.Errorf("a wrong checksum fails: %+v", j)
	}
}

func TestApplyUpdate(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "xtune"), []byte("old app"), 0o755)
	os.WriteFile(filepath.Join(dir, "keep.txt"), []byte("mine"), 0o644)
	// a Linux tarball: everything under xtune/
	var tb bytes.Buffer
	gz := gzip.NewWriter(&tb)
	tw := tar.NewWriter(gz)
	for _, f := range []struct {
		name, body string
		mode       int64
	}{{"xtune/", "", 0o755}, {"xtune/xtune", "new app", 0o755}, {"xtune/lib/libapp.so", "lib", 0o644}} {
		typ := byte(tar.TypeReg)
		if strings.HasSuffix(f.name, "/") {
			typ = tar.TypeDir
		}
		tw.WriteHeader(&tar.Header{Name: f.name, Mode: f.mode, Size: int64(len(f.body)), Typeflag: typ})
		tw.Write([]byte(f.body))
	}
	tw.Close()
	gz.Close()
	arc := filepath.Join(t.TempDir(), "xtune-v9-linux-x64.tar.gz")
	os.WriteFile(arc, tb.Bytes(), 0o600)
	if err := ApplyUpdate(arc, dir); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "xtune")); string(b) != "new app" {
		t.Errorf("replaced: %q", b)
	}
	if fi, _ := os.Stat(filepath.Join(dir, "xtune")); fi.Mode().Perm()&0o100 == 0 {
		t.Error("still executable")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "lib", "libapp.so")); string(b) != "lib" {
		t.Error("new folders made")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "keep.txt")); string(b) != "mine" {
		t.Error("other files left alone")
	}
	// a Windows zip: files at its root; and nothing may land outside the folder
	var zb bytes.Buffer
	zw := zip.NewWriter(&zb)
	w, _ := zw.Create("xtune.exe")
	w.Write([]byte("exe"))
	w, _ = zw.Create("data/app.so")
	w.Write([]byte("so"))
	zw.Close()
	zarc := filepath.Join(t.TempDir(), "x.zip")
	os.WriteFile(zarc, zb.Bytes(), 0o600)
	if err := ApplyUpdate(zarc, dir); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "xtune.exe")); string(b) != "exe" {
		t.Error("zip at the root")
	}
	zb.Reset()
	zw = zip.NewWriter(&zb)
	w, _ = zw.Create("a/../../evil")
	w.Write([]byte("x"))
	zw.Close()
	os.WriteFile(zarc, zb.Bytes(), 0o600)
	if err := ApplyUpdate(zarc, dir); err == nil {
		t.Error("a path out of the folder is refused")
	}
}
