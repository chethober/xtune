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
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
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

// Remote is an SSH connection that can run commands as root (directly or via sudo).
type Remote struct {
	c        *ssh.Client
	Conn     Conn
	isRoot   bool
	sudoPass *string // nil: root login or passwordless sudo
}

func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func Dial(cfg Conn, ask Prompter) (*Remote, error) {
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
	r := &Remote{c: client, Conn: cfg, isRoot: cfg.User == "root"}
	if err := r.setupSudo(ask); err != nil {
		client.Close()
		return nil, err
	}
	return r, nil
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

func (r *Remote) setupSudo(ask Prompter) error {
	if r.isRoot {
		return nil
	}
	if _, _, err := r.Exec("true", "", true); err == nil {
		return nil // passwordless sudo
	}
	try := func(pw string) bool {
		r.sudoPass = &pw
		_, _, err := r.Exec("true", "", true)
		return err == nil
	}
	if r.Conn.Password != "" && try(r.Conn.Password) {
		return nil
	}
	if pw := os.Getenv("XTUNE_SUDO_PASSWORD"); pw != "" && try(pw) {
		return nil
	}
	if ask != nil {
		pw, err := ask(fmt.Sprintf("sudo password for %s", r.Conn.User), true)
		if err != nil {
			return err
		}
		if try(pw) {
			return nil
		}
	}
	r.sudoPass = nil
	return fmt.Errorf("%s cannot run sudo (set XTUNE_SUDO_PASSWORD or log in as root)", r.Conn.User)
}

func (r *Remote) wrap(cmd, stdin string, sudo bool) (string, string) {
	if !sudo || r.isRoot {
		return cmd, stdin
	}
	if r.sudoPass != nil {
		return "sudo -S -p '' bash -c " + shq(cmd), *r.sudoPass + "\n" + stdin
	}
	return "sudo -n bash -c " + shq(cmd), stdin
}

// Exec runs cmd and returns stdout/stderr; stdin is fed to the command.
func (r *Remote) Exec(cmd, stdin string, sudo bool) (string, string, error) {
	s, err := r.c.NewSession()
	if err != nil {
		return "", "", err
	}
	defer s.Close()
	cmd, stdin = r.wrap(cmd, stdin, sudo)
	var out, errb bytes.Buffer
	s.Stdin, s.Stdout, s.Stderr = strings.NewReader(stdin), &out, &errb
	err = s.Run(cmd)
	if err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = strings.TrimSpace(out.String())
		}
		if len(msg) > 1500 {
			msg = msg[len(msg)-1500:]
		}
		err = fmt.Errorf("%w: %s", err, msg)
	}
	return out.String(), errb.String(), err
}

// Root runs cmd as root and returns stdout.
func (r *Remote) Root(cmd string) (string, error) {
	out, _, err := r.Exec(cmd, "", true)
	return out, err
}

// Upload writes content to path as root, atomically, with the given octal mode.
func (r *Remote) Upload(dst string, content []byte, mode string) error {
	cmd := fmt.Sprintf("umask 077; mkdir -p %s && cat > %s.tmp && chmod %s %s.tmp && mv -f %s.tmp %s",
		shq(path.Dir(dst)), shq(dst), mode, shq(dst), shq(dst), shq(dst))
	_, _, err := r.Exec(cmd, string(content), true)
	return err
}

// Stream runs cmd as root and sends each output line to fn until ctx is cancelled or the command exits.
func (r *Remote) Stream(ctx context.Context, cmd string, fn func(string)) error {
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

func (r *Remote) Close() { r.c.Close() }

func expandHome(p string) string {
	if strings.HasPrefix(p, "~") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, strings.TrimPrefix(p, "~"))
	}
	return p
}
