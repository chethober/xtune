package protocols

import "testing"

func TestFlagEmoji(t *testing.T) {
	for cc, want := range map[string]string{"DE": "🇩🇪", "nl": "🇳🇱", "US": "🇺🇸", "": "", "off": "", "D1": "", "DEU": ""} {
		if got := FlagEmoji(cc); got != want {
			t.Errorf("flagEmoji(%q) = %q, want %q", cc, got, want)
		}
	}
}

func TestExpandNameDropsEmptyParts(t *testing.T) {
	vars := map[string]string{"flag": "", "country": "", "server": "", "user": "bob", "id": "r-tcp", "proto": "REALITY", "via": ""}
	for tmpl, want := range map[string]string{
		"{flag} {user}-{id} {via}":            "bob-r-tcp",
		"{flag} {server} · {proto} | {user}":  "REALITY | bob",
		"{server} - {proto} - {via}":          "REALITY",
		"[{proto}] {user}":                    "[REALITY] bob",
		"{flag} {server} {unknown}":           "{unknown}",
		"  {user}   ·   ·  {id}  ":            "bob · r-tcp",
		"{flag}{server}":                      "",
		"{flag} {country} {server} · {proto}": "REALITY",
	} {
		if got := expandName(tmpl, vars); got != want {
			t.Errorf("%q: got %q, want %q", tmpl, got, want)
		}
	}
}
