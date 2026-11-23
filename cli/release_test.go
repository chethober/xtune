package main

import "testing"

func TestNewerPreRelease(t *testing.T) {
	if !newer("v1.7.0", "v1.7.0-rc") || newer("v1.7.0-rc", "v1.7.0") || newer("v26.11.9", "Xray 26.11.9 (Xray) linux/amd64") {
		t.Error("a release is newer than its pre-releases, and only then")
	}
	if normTag("1.7.0-rc") != "v1.7.0-rc" || normTag("Xray 26.11.9 (Xray)") != "v26.11.9" || normTag("v1.6.1") != "v1.6.1" {
		t.Errorf("normTag: %s %s", normTag("1.7.0-rc"), normTag("Xray 26.11.9 (Xray)"))
	}
}
