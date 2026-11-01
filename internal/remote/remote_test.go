package remote

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

func TestLocalTransport(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("no bash")
	}
	var l localTransport
	if out, _, err := l.Exec("cat; echo -n done", "in-", false); err != nil || out != "in-done" {
		t.Errorf("exec: %q %v", out, err)
	}
	if _, _, err := l.Exec("echo boom >&2; exit 3", "", false); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("exec error should carry stderr: %v", err)
	}
	var lines []string
	l.Stream(context.Background(), "printf 'a\\nb\\n'", func(s string) { lines = append(lines, s) })
	if strings.Join(lines, ",") != "a,b" {
		t.Errorf("stream: %v", lines)
	}
}
