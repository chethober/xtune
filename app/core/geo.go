package core

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/chethober/xtune/app/core/geodat"
	"github.com/xtls/xray-core/common/platform"
)

// The app ships a geoip.dat with only these countries (scripts/fetch-geo.sh trims it: the full
// file is 24 MB). A rule for another country reads it from the full file, which the app downloads
// once when asked (Routing shows which countries need it).
var BuiltinGeoIP = []string{"private", "ir", "cn", "ru"}

const (
	geoipURL    = "https://github.com/v2fly/geoip/releases/latest/download/geoip.dat"
	geoipSumURL = geoipURL + ".sha256sum"
)

var (
	geoMu    sync.Mutex
	geoJob   *GeoJob
	geoCodes map[string]bool // the full file's countries
	geoStamp time.Time       // the full file's mtime geoCodes was read from
)

// GeoJob is the full country list's download.
type GeoJob struct {
	State string `json:"state"` // downloading | ready | failed
	Done  int64  `json:"done"`
	Total int64  `json:"total"`
	Error string `json:"error,omitempty"`
}

// GeoStatus says which countries rules can use.
type GeoStatus struct {
	Builtin []string `json:"builtin"`
	Full    bool     `json:"full"` // the full list is downloaded: every country works
	Job     *GeoJob  `json:"job,omitempty"`
}

func geoipFullPath() string {
	dataMu.Lock()
	d := dataDir
	dataMu.Unlock()
	if d == "" {
		return ""
	}
	return filepath.Join(d, "geo", "geoip-full.dat")
}

func hasFullGeoIP() bool {
	p := geoipFullPath()
	if p == "" {
		return false
	}
	_, err := os.Stat(p)
	return err == nil
}

// builtinGeoIP: is the country in the shipped geoip.dat? Lists from before trimming have them all.
func builtinGeoIP(code string) bool {
	if slices.Contains(BuiltinGeoIP, code) {
		return true
	}
	st, err := os.Stat(platform.GetAssetLocation("geoip.dat"))
	return err == nil && st.Size() > 8<<20
}

// geoIPMatch is a geoip rule's "ip" list: geoip:CODE when the shipped file has the country, else
// the full file's entry. A country Xray cannot find would fail the whole connection, so it is
// checked here and the rule is left out instead.
func geoIPMatch(v string) ([]string, error) {
	code := strings.ToLower(strings.TrimSpace(v))
	neg := strings.HasPrefix(code, "!")
	bare := strings.TrimPrefix(code, "!")
	if bare == "" || strings.ContainsAny(bare, ":/\\ ") {
		return nil, fmt.Errorf("bad country %q", v)
	}
	if builtinGeoIP(bare) {
		return []string{"geoip:" + code}, nil
	}
	p := geoipFullPath()
	if !hasFullGeoIP() {
		return nil, fmt.Errorf("country %q is not built in: download the full country list", bare)
	}
	codes, err := fullCodes(p)
	if err != nil {
		return nil, err
	}
	if !codes[bare] {
		return nil, fmt.Errorf("no country %q", bare)
	}
	if rel, ok := relAsset(p); ok {
		return []string{"ext:" + rel + ":" + code}, nil
	}
	if neg { // the ranges themselves cannot say "not"
		return nil, fmt.Errorf("\"not %s\" needs the country list next to the app", bare)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	return geodat.CIDRs(b, bare)
}

// relAsset is p relative to Xray's asset folder, where it looks up ext: files. Xray splits ext:
// on ":", so a path on another drive (Windows) cannot be named this way.
func relAsset(p string) (string, bool) {
	base := filepath.Dir(platform.GetAssetLocation("geoip.dat"))
	r, err := filepath.Rel(base, p)
	if err != nil || filepath.IsAbs(r) || strings.Contains(r, ":") {
		return "", false
	}
	return filepath.ToSlash(r), true
}

// fullCodes are the full file's countries (lower case), kept until the file changes.
func fullCodes(p string) (map[string]bool, error) {
	st, err := os.Stat(p)
	if err != nil {
		return nil, err
	}
	geoMu.Lock()
	defer geoMu.Unlock()
	if geoCodes != nil && st.ModTime().Equal(geoStamp) {
		return geoCodes, nil
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	list, err := geodat.Codes(b)
	if err != nil {
		return nil, fmt.Errorf("the country list is damaged (%v): download it again", err)
	}
	m := map[string]bool{}
	for _, c := range list {
		m[strings.ToLower(c)] = true
	}
	geoCodes, geoStamp = m, st.ModTime()
	return m, nil
}

// GeoInfo is what Routing shows: the countries built in, and whether the full list is here.
func GeoInfo() GeoStatus {
	s := GeoStatus{Builtin: BuiltinGeoIP, Full: hasFullGeoIP()}
	geoMu.Lock()
	if geoJob != nil {
		j := *geoJob
		s.Job = &j
	}
	geoMu.Unlock()
	return s
}

// StartGeoDownload fetches the full geoip.dat (directly, or through the connection when GitHub is
// filtered) and checks it against its published SHA-256.
func StartGeoDownload() (GeoStatus, error) {
	p := geoipFullPath()
	if p == "" {
		return GeoStatus{}, errors.New("the app has not told the core where its data goes")
	}
	geoMu.Lock()
	if geoJob == nil || geoJob.State != "downloading" {
		geoJob = &GeoJob{State: "downloading"}
		j := geoJob
		go func() {
			defer recovered("download the country list")
			err := geoDownload(p, j)
			geoMu.Lock()
			if err != nil {
				j.State, j.Error = "failed", err.Error()
			} else {
				j.State = "ready"
			}
			geoMu.Unlock()
		}()
	}
	geoMu.Unlock()
	return GeoInfo(), nil
}

func geoDownload(p string, j *GeoJob) error {
	resp, err := updateGet(geoipSumURL, time.Minute)
	if err != nil {
		return err
	}
	line, err := io.ReadAll(io.LimitReader(resp.Body, 1024))
	resp.Body.Close()
	if err != nil {
		return err
	}
	want := strings.ToLower(strings.Fields(string(line) + " ")[0])
	if len(want) != 64 {
		return errors.New("the country list's checksum could not be read")
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	resp, err = updateGet(geoipURL, 20*time.Minute)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	geoMu.Lock()
	j.Total = resp.ContentLength
	geoMu.Unlock()
	part := p + ".part"
	f, err := os.OpenFile(part, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	h := sha256.New()
	buf := make([]byte, 64<<10)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, err := f.Write(buf[:n]); err != nil {
				f.Close()
				os.Remove(part)
				return err
			}
			h.Write(buf[:n])
			geoMu.Lock()
			j.Done += int64(n)
			geoMu.Unlock()
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			f.Close()
			os.Remove(part)
			return rerr
		}
	}
	f.Close()
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		os.Remove(part)
		return fmt.Errorf("the download does not match its checksum (%s…): try again", got[:12])
	}
	return os.Rename(part, p)
}

// DeleteFullGeoIP frees the space the full list takes.
func DeleteFullGeoIP() error {
	p := geoipFullPath()
	if p == "" {
		return nil
	}
	geoMu.Lock()
	geoJob = nil
	geoMu.Unlock()
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
