package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCrashLog(t *testing.T) {
	dir := t.TempDir()
	SetDataDir(dir)
	defer SetDataDir("")

	// a fatal crash the runtime wrote during the last run becomes an entry at the next setup
	os.WriteFile(filepath.Join(dir, "crash", "go-crash.txt"), []byte("panic: boom\n\ngoroutine 7 [running]:\nmain.f()\n"), 0o600)
	SetDataDir(dir)
	if b, _ := os.ReadFile(filepath.Join(dir, "crash", "go-crash.txt")); len(b) != 0 {
		t.Fatalf("go-crash.txt not started afresh: %q", b)
	}

	// an entry the Android side wrote
	os.WriteFile(filepath.Join(dir, "crash", "1700000000000-android-crash.txt"),
		[]byte("kind: crash\nsource: android\nwhere: thread main\napp: 1.6.0\n\njava.lang.IllegalStateException: Reply already submitted\n\tat x.y\n"), 0o600)

	done := make(chan struct{})
	goSafe("a test goroutine", func() { defer close(done); var m map[string]int; m["x"] = 1 })
	<-done
	time.Sleep(50 * time.Millisecond) // the deferred recover writes after close(done)

	RecordCrash("dart", "error", "home", "Bad state: vless://secret@1.2.3.4:443 broke")

	l := CrashList(0)
	if len(l) != 4 {
		t.Fatalf("want 4 entries, got %d: %+v", len(l), l)
	}
	var kinds []string
	for _, e := range l {
		kinds = append(kinds, e.Source+"/"+e.Kind)
		if strings.Contains(e.Text, "secret@") {
			t.Errorf("not redacted: %q", e.Text)
		}
	}
	got := strings.Join(kinds, " ")
	for _, want := range []string{"go/crash", "android/crash", "go/error", "dart/error"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in %s", want, got)
		}
	}
	if l[len(l)-1].Title != "java.lang.IllegalStateException: Reply already submitted" {
		t.Errorf("oldest title %q", l[len(l)-1].Title)
	}
	if !strings.Contains(Diagnostics(nil), "== crash log") {
		t.Error("diagnostics leaves out the crash log")
	}
	ClearCrashes()
	if l := CrashList(0); len(l) != 0 {
		t.Fatalf("clear left %d", len(l))
	}
}

func TestCrashLogKeepsNewest(t *testing.T) {
	dir := t.TempDir()
	SetDataDir(dir)
	defer SetDataDir("")
	for i := 0; i < crashKeep+5; i++ {
		writeCrash(filepath.Join(dir, "crash"), time.UnixMilli(int64(1700000000000+i)), "go", "error", "x", "panic")
	}
	if n := len(CrashList(0)); n != crashKeep {
		t.Fatalf("kept %d, want %d", n, crashKeep)
	}
	if CrashList(1)[0].At != int64(1700000000000+crashKeep+4) {
		t.Fatal("did not keep the newest")
	}
}
