package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"rsc.io/qr"
)

const releases = "https://github.com/chethober/xtune/releases/download/"

// LatestXtune is the newest xtune release tag.
func LatestXtune() (string, error) {
	body, err := httpGet("https://api.github.com/repos/chethober/xtune/releases/latest", 20*time.Second)
	if err != nil {
		return "", err
	}
	var rel struct {
		TagName string `json:"tag_name"`
	}
	if err := json.Unmarshal(body, &rel); err != nil || rel.TagName == "" {
		return "", fmt.Errorf("could not read the latest xtune release")
	}
	return rel.TagName, nil
}

// SelfUpdate replaces the running binary with release tag, checksum-verified. It reports false
// when this binary is already that version.
func SelfUpdate(tag string, log func(string)) (bool, error) {
	if !newer(tag, version) && version != "dev" {
		return false, nil
	}
	name := fmt.Sprintf("xtune_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		name = strings.TrimSuffix(name, ".tar.gz") + ".zip"
	}
	log("downloading xtune " + tag + " (" + name + ")")
	archive, err := httpGet(releases+tag+"/"+name, 5*time.Minute)
	if err != nil {
		return false, err
	}
	sums, err := httpGet(releases+tag+"/checksums.txt", time.Minute)
	if err != nil {
		return false, err
	}
	if err := verifySum(sums, name, archive); err != nil {
		return false, err
	}
	bin, err := extractXtune(name, archive)
	if err != nil {
		return false, err
	}
	exe, err := os.Executable()
	if err != nil {
		return false, err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return false, err
	}
	return true, replaceExe(exe, bin)
}

func verifySum(sums []byte, name string, data []byte) error {
	sum := sha256.Sum256(data)
	for _, l := range strings.Split(string(sums), "\n") {
		if f := strings.Fields(l); len(f) == 2 && f[1] == name {
			if f[0] != hex.EncodeToString(sum[:]) {
				return fmt.Errorf("checksum mismatch for %s", name)
			}
			return nil
		}
	}
	return fmt.Errorf("%s is not in the release checksums", name)
}

func extractXtune(name string, archive []byte) ([]byte, error) {
	if strings.HasSuffix(name, ".zip") {
		zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
		if err != nil {
			return nil, err
		}
		for _, f := range zr.File {
			if filepath.Base(f.Name) == "xtune.exe" {
				rc, err := f.Open()
				if err != nil {
					return nil, err
				}
				defer rc.Close()
				return io.ReadAll(io.LimitReader(rc, 200<<20))
			}
		}
		return nil, fmt.Errorf("no xtune.exe in %s", name)
	}
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err != nil {
			return nil, fmt.Errorf("no xtune binary in %s: %v", name, err)
		}
		if filepath.Base(h.Name) == "xtune" && h.Typeflag == tar.TypeReg {
			return io.ReadAll(io.LimitReader(tr, 200<<20))
		}
	}
}

// replaceExe swaps in the new binary next to the old one. Windows cannot overwrite a running
// program, but it can rename it, so the old one is moved aside first.
func replaceExe(exe string, bin []byte) error {
	tmp := exe + ".new"
	if err := os.WriteFile(tmp, bin, 0o755); err != nil {
		if os.IsPermission(err) {
			return fmt.Errorf("cannot write %s: run the update with the rights that installed it (sudo xtune self-update)", filepath.Dir(exe))
		}
		return err
	}
	if runtime.GOOS == "windows" {
		old := exe + ".old"
		os.Remove(old)
		if err := os.Rename(exe, old); err != nil {
			os.Remove(tmp)
			return err
		}
	}
	if err := os.Rename(tmp, exe); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// ---------------------------------------------------------------- QR codes in the terminal

// printQR draws text as a QR code with half-block characters and explicit black and white
// colours, so it scans on dark and light terminals alike.
func printQR(w io.Writer, text string) error {
	c, err := qr.Encode(text, qr.L)
	if err != nil {
		return err
	}
	const quiet = 2
	black := func(x, y int) bool {
		x, y = x-quiet, y-quiet
		return x >= 0 && y >= 0 && x < c.Size && y < c.Size && c.Black(x, y)
	}
	n := c.Size + 2*quiet
	var b strings.Builder
	for y := 0; y < n; y += 2 {
		for x := 0; x < n; x++ {
			fg, bg := "97", "107" // top and bottom module: white
			if black(x, y) {
				fg = "30"
			}
			if black(x, y+1) {
				bg = "40"
			}
			fmt.Fprintf(&b, "\x1b[%s;%sm▀", fg, bg)
		}
		b.WriteString("\x1b[0m\n")
	}
	_, err = io.WriteString(w, b.String())
	return err
}
