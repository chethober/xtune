package core

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Diagnostics: Xray's log in the app's data directory, and a bundle for bug reports with every
// secret taken out.

var (
	dataMu  sync.Mutex
	dataDir string
)

// SetDataDir is where the core keeps its log.
func SetDataDir(dir string) {
	dataMu.Lock()
	defer dataMu.Unlock()
	if dir != "" {
		os.MkdirAll(dir, 0o700)
	}
	dataDir = dir
}

// logFile is Xray's log file, started afresh when it grows past a megabyte.
func logFile() string {
	dataMu.Lock()
	defer dataMu.Unlock()
	if dataDir == "" {
		return ""
	}
	f := filepath.Join(dataDir, "xray.log")
	if st, err := os.Stat(f); err == nil && st.Size() > 1<<20 {
		os.Rename(f, f+".old")
	}
	return f
}

// LogTail is the last n bytes of the log (and the previous file's end, when the log just turned
// over), secrets taken out.
func LogTail(n int64) string {
	f := logFile()
	if f == "" {
		return ""
	}
	if n <= 0 {
		n = 64 << 10
	}
	read := func(path string, max int64) string {
		fh, err := os.Open(path)
		if err != nil {
			return ""
		}
		defer fh.Close()
		st, _ := fh.Stat()
		if st.Size() > max {
			fh.Seek(st.Size()-max, io.SeekStart)
		}
		b, _ := io.ReadAll(fh)
		return string(b)
	}
	s := read(f, n)
	if int64(len(s)) < n/2 {
		s = read(f+".old", n-int64(len(s))) + s
	}
	return Redact(s)
}

var (
	uuidRe   = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)
	linkRe   = regexp.MustCompile(`(?i)\b(vless|vmess|trojan|ss|hysteria2|hy2|wireguard|ssh|tuic)://\S+`)
	tokenRe  = regexp.MustCompile(`(https?://[^/\s]+/[^/\s]+/)[A-Za-z0-9_-]{16,}`)
	keyRe    = regexp.MustCompile(`(?i)("(password|secretKey|publicKey|preSharedKey|privateKey|private_key|key|auth|id|shortId|uapi|pubKey|hostKey|keyPass)"\s*:\s*)"[^"]*"`)
	ipv4Re   = regexp.MustCompile(`\b(\d{1,3})\.(\d{1,3})\.\d{1,3}\.\d{1,3}\b`)
	emailRe  = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)
	sshKeyRe = regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----`)
)

// Redact takes secrets out of text meant for a bug report: user IDs, share links, subscription
// tokens, keys and passwords, private keys, e-mail addresses, and the last two bytes of IPv4
// addresses (the network stays visible, the host does not).
func Redact(s string) string {
	s = sshKeyRe.ReplaceAllString(s, "[private key]")
	s = linkRe.ReplaceAllString(s, "$1://[link]")
	s = tokenRe.ReplaceAllString(s, "${1}[token]")
	s = keyRe.ReplaceAllString(s, `$1"[secret]"`)
	s = uuidRe.ReplaceAllString(s, "[id]")
	s = emailRe.ReplaceAllString(s, "[email]")
	s = ipv4Re.ReplaceAllStringFunc(s, func(ip string) string {
		m := ipv4Re.FindStringSubmatch(ip)
		if m[1] == "127" || m[1] == "10" || m[1] == "0" {
			return ip
		}
		return m[1] + "." + m[2] + ".x.x"
	})
	return s
}

// Diagnostics is a bug report: versions, the state of the connection, the Xray config the
// connection uses (redacted), and the log. extra is what the app adds (its settings).
func Diagnostics(extra map[string]any) string {
	var b strings.Builder
	fmt.Fprintf(&b, "xtune client diagnostics, %s\n\ncore %s, %s/%s, %s\n\n", time.Now().UTC().Format(time.RFC3339), Version, runtime.GOOS, runtime.GOARCH, runtime.Version())
	st := CurrentStatus()
	sj, _ := json.MarshalIndent(st, "", "  ")
	fmt.Fprintf(&b, "== status\n%s\n\n", sj)
	if n, err := CurrentNetwork(); err == nil {
		fmt.Fprintf(&b, "== network\n%s %s\n\n", n.ASN, n.Name)
	} else {
		fmt.Fprintf(&b, "== network\nlookup failed: %v\n\n", err)
	}
	if len(extra) > 0 {
		ej, _ := json.MarshalIndent(extra, "", "  ")
		fmt.Fprintf(&b, "== app\n%s\n\n", Redact(string(ej)))
	}
	engine.mu.Lock()
	p, o, cs := engine.profile, engine.opts, engine.configs
	engine.mu.Unlock()
	if p != nil {
		fmt.Fprintf(&b, "== profile\n%d configs:", len(p.Configs))
		for _, c := range p.Configs {
			fmt.Fprintf(&b, " %s(%s,%s)", c.ID, c.Kind, c.Engine)
		}
		cfg := BuildConfig(p, o, cs, map[string]int{}, nil)
		cj, _ := json.MarshalIndent(cfg, "", "  ")
		fmt.Fprintf(&b, "\n\n== xray config\n%s\n\n", Redact(string(cj)))
	}
	fmt.Fprintf(&b, "== log\n%s\n", LogTail(96<<10))
	return b.String()
}
