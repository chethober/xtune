package remote

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestTestUnitReplacesLoadedUnit(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash unavailable")
	}
	b, err := os.ReadFile("helper.sh")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	start := strings.Index(s, "start_test_unit() {")
	end := strings.Index(s[start:], "\ntest_stop_extras() {")
	script := `loaded=1
systemctl() {
 case "$1" in
 stop) stopped=1 ;;
 reset-failed) [ "$stopped" = 1 ] || exit 10; loaded=0 ;;
 esac
}
systemd-run() {
 [ "$loaded" = 0 ] || exit 11
 [[ " $* " == *" --collect "* ]] || exit 12
 [[ " $* " == *" --unit=xray-autotune-test-tunnels "* ]] || exit 13
 loaded=1
}
` + s[start:start+end] + "\nstart_test_unit xray-autotune-test-tunnels /bin/true\nstart_test_unit xray-autotune-test-tunnels /bin/true\n"
	if out, err := exec.Command("bash", "-eu", "-c", script).CombinedOutput(); err != nil {
		t.Fatalf("restart: %v %s", err, out)
	}
}
