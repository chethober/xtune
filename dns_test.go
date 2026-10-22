package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestFallbackNameservers(t *testing.T) {
	rc := filepath.Join(t.TempDir(), "resolv.conf")
	os.WriteFile(rc, []byte("# termux\nnameserver 8.8.8.8\nnameserver ::1\nnameserver 2001:4860:4860::8888\n"), 0o644)
	got := fallbackNameservers(" 10.0.0.1, 127.0.0.1,9.9.9.9:5353,dns.google", rc)
	want := []string{"10.0.0.1:53", "9.9.9.9:5353", "8.8.8.8:53", "[2001:4860:4860::8888]:53",
		"1.1.1.1:53", "9.9.9.9:53", "208.67.222.222:53"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
	if got := fallbackNameservers("", ""); len(got) != len(publicDNS) {
		t.Fatalf("no env, no resolv.conf: %v", got)
	}
}
