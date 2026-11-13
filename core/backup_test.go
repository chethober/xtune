package core

import (
	"strings"
	"testing"
)

func TestBackupSealOpen(t *testing.T) {
	data := `{"subs":[{"url":"https://x/p/tok"}],"servers":[{"host":"203.0.113.1","password":"hunter22"}]}`
	if _, err := SealBackup(data, "short"); err == nil {
		t.Error("a short passphrase is refused")
	}
	s, err := SealBackup(data, "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(s, backupPrefix) || strings.Contains(s, "hunter22") || strings.Contains(s, "203.0.113.1") {
		t.Errorf("sealed: %s", s)
	}
	if got, err := OpenBackup(s, "correct horse battery"); err != nil || got != data {
		t.Errorf("open: %v %q", err, got)
	}
	if _, err := OpenBackup(s, "wrong horse battery"); err == nil {
		t.Error("a wrong passphrase fails")
	}
	if _, err := OpenBackup(s[:len(s)-8]+"AAAAAAAA", "correct horse battery"); err == nil {
		t.Error("tampering is caught")
	}
	if _, err := OpenBackup("hello", "x"); err == nil {
		t.Error("not a backup")
	}
}
