// Package remote runs commands on the server xtune manages: over SSH from a client, or directly
// when xtune runs on the server itself. Everything that changes the server goes through Helper,
// which runs the embedded helper.sh as root.
package remote

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"runtime"
	"strings"
)

// Dir is where xtune keeps everything on the server.
const Dir = "/opt/xray-autotune"

// Transport runs shell commands on the managed server: over SSH from a client, or directly
// when xtune runs on the server itself.
type Transport interface {
	Exec(cmd, stdin string, sudo bool) (string, string, error)
	Stream(ctx context.Context, cmd string, fn func(string)) error
	Close()
}

// Server is the machine xtune manages. Commands that need root go through Root/Upload/Helper.
type Server struct {
	Transport Transport
	Conn      Conn
	Local     bool // xtune is running on the server itself
}

// Name is how the server is shown in titles.
func (r *Server) Name() string {
	if r.Local {
		return "this server"
	}
	return r.Conn.User + "@" + r.Conn.Host
}

// Exec runs cmd and returns stdout/stderr; stdin is fed to the command.
func (r *Server) Exec(cmd, stdin string, sudo bool) (string, string, error) {
	return r.Transport.Exec(cmd, stdin, sudo)
}

// Root runs cmd as root and returns stdout.
func (r *Server) Root(cmd string) (string, error) {
	out, _, err := r.Transport.Exec(cmd, "", true)
	return out, err
}

// Upload writes content to path as root, atomically, with the given octal mode.
func (r *Server) Upload(dst string, content []byte, mode string) error {
	cmd := fmt.Sprintf("umask 077; mkdir -p %s && cat > %s.tmp && chmod %s %s.tmp && mv -f %s.tmp %s",
		Quote(path.Dir(dst)), Quote(dst), mode, Quote(dst), Quote(dst), Quote(dst))
	_, _, err := r.Transport.Exec(cmd, string(content), true)
	return err
}

// Stream runs cmd as root and sends each output line to fn until ctx is cancelled or the command exits.
func (r *Server) Stream(ctx context.Context, cmd string, fn func(string)) error {
	return r.Transport.Stream(ctx, cmd, fn)
}

func (r *Server) Close() { r.Transport.Close() }

// Lock serializes state changes between local xtune processes on the server (TUI, CLI and the
// enforce timer). Changes a client makes over SSH are not locked; the next enforce run heals them.
func (r *Server) Lock() func() {
	if !r.Local {
		return func() {}
	}
	return flock(Dir + "/.lock")
}

func Quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func cmdErr(err error, stdout, stderr string) error {
	msg := strings.TrimSpace(stderr)
	if msg == "" {
		msg = strings.TrimSpace(stdout)
	}
	if len(msg) > 1500 {
		msg = msg[len(msg)-1500:]
	}
	return fmt.Errorf("%w: %s", err, msg)
}

// ---------------------------------------------------------------- local (server mode)

type localTransport struct{}

// OpenLocal manages the server xtune is running on. It needs root, like the helper it runs.
func OpenLocal() (*Server, error) {
	if runtime.GOOS != "linux" {
		return nil, errors.New("server mode needs Linux; on your own computer run xtune and connect to the server")
	}
	if os.Geteuid() != 0 {
		return nil, errors.New("server mode needs root: run sudo xtune")
	}
	host, _ := os.Hostname()
	return &Server{Transport: localTransport{}, Conn: Conn{Name: host, Host: host, User: "root"}, Local: true}, nil
}

// Installed reports whether this machine is an xtune-managed server.
func Installed() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	_, err := os.Stat(Dir)
	return err == nil
}

func (localTransport) Exec(cmd, stdin string, _ bool) (string, string, error) {
	c := exec.Command("bash", "-c", cmd)
	var out, errb strings.Builder
	c.Stdin, c.Stdout, c.Stderr = strings.NewReader(stdin), &out, &errb
	err := c.Run()
	if err != nil {
		err = cmdErr(err, out.String(), errb.String())
	}
	return out.String(), errb.String(), err
}

func (localTransport) Stream(ctx context.Context, cmd string, fn func(string)) error {
	c := exec.CommandContext(ctx, "bash", "-c", cmd)
	pr, pw := io.Pipe()
	c.Stdout, c.Stderr = pw, pw
	if err := c.Start(); err != nil {
		return err
	}
	go func() { _ = c.Wait(); pw.Close() }()
	sc := bufio.NewScanner(pr)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		fn(sc.Text())
	}
	return nil
}

func (localTransport) Close() {}
