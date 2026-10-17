package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/muesli/cancelreader"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
	"golang.org/x/term"
)

// Conn describes how to reach a server. Secrets are never written to disk.
type Conn struct {
	Name     string `json:"name"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	User     string `json:"user"`
	KeyPath  string `json:"key,omitempty"`
	Password string `json:"-"`
}

// Prompter asks the user for a value; secret input is not echoed.
type Prompter func(label string, secret bool) (string, error)

// sshTransport is an SSH connection that can run commands as root (directly or via sudo).
type sshTransport struct {
	c        *ssh.Client
	user     string
	password string
	isRoot   bool
	sudoPass *string // nil: root login or passwordless sudo
}

func Dial(cfg Conn, ask Prompter) (*Server, error) {
	if cfg.Port == 0 {
		cfg.Port = 22
	}
	if cfg.User == "" {
		cfg.User = "root"
	}
	hostKeys, err := hostKeyCallback()
	if err != nil {
		return nil, err
	}
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	try := func(auth []ssh.AuthMethod) (*ssh.Client, error) {
		return ssh.Dial("tcp", addr, &ssh.ClientConfig{User: cfg.User, Auth: auth, HostKeyCallback: hostKeys, Timeout: 15 * time.Second})
	}

	var client *ssh.Client
	if cfg.Password != "" {
		client, err = try(passwordAuth(cfg.Password))
	} else {
		auth, aerr := keyAuth(cfg.KeyPath, ask)
		if aerr != nil {
			return nil, aerr
		}
		if len(auth) > 0 {
			client, err = try(auth)
		}
		if client == nil && ask != nil { // no usable key: fall back to a password
			if err != nil && !isAuthErr(err) {
				return nil, err
			}
			pw, perr := ask(fmt.Sprintf("SSH password for %s@%s", cfg.User, cfg.Host), true)
			if perr != nil {
				return nil, perr
			}
			cfg.Password = pw
			client, err = try(passwordAuth(pw))
		}
	}
	if err != nil {
		return nil, fmt.Errorf("ssh %s: %w", addr, err)
	}
	if client == nil {
		return nil, fmt.Errorf("ssh %s: no authentication method available (pass --key or --password-env)", addr)
	}
	t := &sshTransport{c: client, user: cfg.User, password: cfg.Password, isRoot: cfg.User == "root"}
	if err := t.setupSudo(ask); err != nil {
		client.Close()
		return nil, err
	}
	return &Server{sh: t, Conn: cfg}, nil
}

func isAuthErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), "unable to authenticate")
}

func passwordAuth(pw string) []ssh.AuthMethod {
	return []ssh.AuthMethod{ssh.Password(pw), ssh.KeyboardInteractive(
		func(_, _ string, qs []string, _ []bool) ([]string, error) {
			ans := make([]string, len(qs))
			for i := range ans {
				ans[i] = pw
			}
			return ans, nil
		})}
}

func keyAuth(keyPath string, ask Prompter) ([]ssh.AuthMethod, error) {
	var methods []ssh.AuthMethod
	if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" && keyPath == "" {
		if c, err := net.Dial("unix", sock); err == nil {
			methods = append(methods, ssh.PublicKeysCallback(agent.NewClient(c).Signers))
		}
	}
	paths := []string{keyPath}
	if keyPath == "" {
		home, _ := os.UserHomeDir()
		paths = nil
		for _, n := range []string{"id_ed25519", "id_ecdsa", "id_rsa"} {
			paths = append(paths, filepath.Join(home, ".ssh", n))
		}
	}
	var signers []ssh.Signer
	for _, p := range paths {
		raw, err := os.ReadFile(expandHome(p))
		if err != nil {
			if keyPath != "" {
				return nil, fmt.Errorf("read key %s: %w", p, err)
			}
			continue
		}
		s, err := ssh.ParsePrivateKey(raw)
		var missing *ssh.PassphraseMissingError
		if errors.As(err, &missing) && ask != nil {
			pass, perr := ask("Passphrase for "+p, true)
			if perr != nil {
				return nil, perr
			}
			s, err = ssh.ParsePrivateKeyWithPassphrase(raw, []byte(pass))
		}
		if err != nil {
			if keyPath != "" {
				return nil, fmt.Errorf("parse key %s: %w", p, err)
			}
			continue
		}
		signers = append(signers, s)
	}
	if len(signers) > 0 {
		methods = append(methods, ssh.PublicKeys(signers...))
	}
	return methods, nil
}

// hostKeyCallback trusts ~/.ssh/known_hosts and records unknown hosts on first use;
// a changed key is always an error.
func hostKeyCallback() (ssh.HostKeyCallback, error) {
	home, _ := os.UserHomeDir()
	khPath := filepath.Join(home, ".ssh", "known_hosts")
	if err := os.MkdirAll(filepath.Dir(khPath), 0o700); err != nil {
		return nil, err
	}
	if f, err := os.OpenFile(khPath, os.O_CREATE|os.O_APPEND, 0o600); err == nil {
		f.Close()
	}
	kh, err := knownhosts.New(khPath)
	if err != nil {
		return nil, err
	}
	return func(host string, remote net.Addr, key ssh.PublicKey) error {
		err := kh(host, remote, key)
		var ke *knownhosts.KeyError
		if errors.As(err, &ke) && len(ke.Want) == 0 {
			f, ferr := os.OpenFile(khPath, os.O_APPEND|os.O_WRONLY, 0o600)
			if ferr != nil {
				return ferr
			}
			defer f.Close()
			_, ferr = fmt.Fprintln(f, knownhosts.Line([]string{knownhosts.Normalize(host)}, key))
			return ferr
		}
		if errors.As(err, &ke) {
			return fmt.Errorf("HOST KEY CHANGED for %s - refusing to connect (check %s)", host, khPath)
		}
		return err
	}, nil
}

func (r *sshTransport) setupSudo(ask Prompter) error {
	if r.isRoot {
		return nil
	}
	if _, _, err := r.exec("true", "", true); err == nil {
		return nil // passwordless sudo
	}
	try := func(pw string) bool {
		r.sudoPass = &pw
		_, _, err := r.exec("true", "", true)
		return err == nil
	}
	if r.password != "" && try(r.password) {
		return nil
	}
	if pw := os.Getenv("XTUNE_SUDO_PASSWORD"); pw != "" && try(pw) {
		return nil
	}
	if ask != nil {
		pw, err := ask(fmt.Sprintf("sudo password for %s", r.user), true)
		if err != nil {
			return err
		}
		if try(pw) {
			return nil
		}
	}
	r.sudoPass = nil
	return fmt.Errorf("%s cannot run sudo (set XTUNE_SUDO_PASSWORD or log in as root)", r.user)
}

func (r *sshTransport) wrap(cmd, stdin string, sudo bool) (string, string) {
	if !sudo || r.isRoot {
		return cmd, stdin
	}
	if r.sudoPass != nil {
		return "sudo -S -p '' bash -c " + shq(cmd), *r.sudoPass + "\n" + stdin
	}
	return "sudo -n bash -c " + shq(cmd), stdin
}

func (r *sshTransport) exec(cmd, stdin string, sudo bool) (string, string, error) {
	s, err := r.c.NewSession()
	if err != nil {
		return "", "", err
	}
	defer s.Close()
	cmd, stdin = r.wrap(cmd, stdin, sudo)
	var out, errb bytes.Buffer
	s.Stdin, s.Stdout, s.Stderr = strings.NewReader(stdin), &out, &errb
	if err = s.Run(cmd); err != nil {
		err = cmdErr(err, out.String(), errb.String())
	}
	return out.String(), errb.String(), err
}

func (r *sshTransport) stream(ctx context.Context, cmd string, fn func(string)) error {
	s, err := r.c.NewSession()
	if err != nil {
		return err
	}
	defer s.Close()
	cmd, stdin := r.wrap(cmd, "", true)
	s.Stdin = strings.NewReader(stdin)
	pr, pw := io.Pipe()
	s.Stdout, s.Stderr = pw, pw
	if err := s.Start(cmd); err != nil {
		return err
	}
	go func() { _ = s.Wait(); pw.Close() }()
	go func() { <-ctx.Done(); s.Close(); pw.Close() }()
	sc := bufio.NewScanner(pr)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		fn(sc.Text())
	}
	return nil
}

func (r *sshTransport) close() { r.c.Close() }

// Console runs cmd on the server in an interactive terminal; this is how a client reaches the
// server-side TUI. sudo, when needed, prompts inside that terminal.
func (r *Server) Console(cmd string) error {
	t, ok := r.sh.(*sshTransport)
	if !ok {
		return errors.New("the console needs an SSH connection")
	}
	in, out := int(os.Stdin.Fd()), int(os.Stdout.Fd())
	if !term.IsTerminal(in) {
		return errors.New("the console needs an interactive terminal")
	}
	if !t.isRoot {
		cmd = "sudo " + cmd
	}
	s, err := t.c.NewSession()
	if err != nil {
		return err
	}
	defer s.Close()
	w, h, err := term.GetSize(out)
	if err != nil {
		w, h = 80, 24
	}
	name := os.Getenv("TERM")
	if name == "" {
		name = "xterm-256color"
	}
	if err := s.RequestPty(name, h, w, ssh.TerminalModes{ssh.ECHO: 1, ssh.TTY_OP_ISPEED: 14400, ssh.TTY_OP_OSPEED: 14400}); err != nil {
		return err
	}
	stdin, err := s.StdinPipe()
	if err != nil {
		return err
	}
	s.Stdout, s.Stderr = os.Stdout, os.Stderr
	old, err := term.MakeRaw(in)
	if err != nil {
		return err
	}
	defer term.Restore(in, old)
	// a cancelable reader, so the copy goroutine does not swallow the next keypress after the session ends
	cr, err := cancelreader.NewReader(os.Stdin)
	if err != nil {
		return err
	}
	defer cr.Close()
	go io.Copy(stdin, cr)
	done := make(chan struct{})
	defer close(done)
	go func() { // follow terminal resizes (polling works on every OS, unlike SIGWINCH)
		tk := time.NewTicker(500 * time.Millisecond)
		defer tk.Stop()
		for {
			select {
			case <-done:
				return
			case <-tk.C:
				if nw, nh, err := term.GetSize(out); err == nil && (nw != w || nh != h) {
					w, h = nw, nh
					s.WindowChange(h, w)
				}
			}
		}
	}()
	err = s.Run(cmd)
	cr.Cancel()
	return err
}

func expandHome(p string) string {
	if strings.HasPrefix(p, "~") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, strings.TrimPrefix(p, "~"))
	}
	return p
}
