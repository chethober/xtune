package core

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/chethober/xtune/app/core/geodat"
	"github.com/xtls/xray-core/app/router"
	xcore "github.com/xtls/xray-core/core"
	"google.golang.org/protobuf/proto"
)

func geoFile(t *testing.T, codes ...string) []byte {
	t.Helper()
	var l router.GeoIPList
	for i, c := range codes {
		l.Entry = append(l.Entry, &router.GeoIP{CountryCode: strings.ToUpper(c), Cidr: []*router.CIDR{
			{Ip: []byte{10, byte(i), 0, 0}, Prefix: 16},
			{Ip: append(make([]byte, 15), byte(i+1)), Prefix: 128},
		}})
	}
	b, err := proto.Marshal(&l)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestGeodatTrim(t *testing.T) {
	full := geoFile(t, "PRIVATE", "IR", "US", "CN", "RU", "DE")
	b, err := geodat.Trim(full, BuiltinGeoIP)
	if err != nil {
		t.Fatal(err)
	}
	codes, _ := geodat.Codes(b)
	if !slices.Equal(codes, []string{"PRIVATE", "IR", "CN", "RU"}) {
		t.Fatalf("kept %v", codes)
	}
	var l router.GeoIPList // still a file Xray reads
	if err := proto.Unmarshal(b, &l); err != nil || len(l.Entry) != 4 {
		t.Fatalf("%v %d", err, len(l.Entry))
	}
	if _, err := geodat.Trim(full, []string{"ir", "xx"}); err == nil {
		t.Fatal("a missing code should fail the build, not ship a file without it")
	}
	got, err := geodat.CIDRs(full, "us")
	if err != nil || !slices.Equal(got, []string{"10.2.0.0/16", "::3/128"}) {
		t.Fatalf("%v %v", got, err)
	}
}

// setGeo points Xray at a trimmed list in assets/ and the app's data at data/.
func setGeo(t *testing.T) (data string) {
	t.Helper()
	d := t.TempDir()
	assets, data := filepath.Join(d, "assets"), filepath.Join(d, "data")
	os.MkdirAll(assets, 0o700)
	b, _ := geodat.Trim(geoFile(t, "PRIVATE", "IR", "CN", "RU"), BuiltinGeoIP)
	os.WriteFile(filepath.Join(assets, "geoip.dat"), b, 0o600)
	t.Setenv("xray.location.asset", assets)
	SetDataDir(data)
	t.Cleanup(func() { SetDataDir("") })
	return data
}

func TestGeoIPRules(t *testing.T) {
	data := setGeo(t)
	if ips, err := geoIPMatch("IR"); err != nil || !slices.Equal(ips, []string{"geoip:ir"}) {
		t.Fatalf("built in: %v %v", ips, err)
	}
	if _, err := geoIPMatch("us"); err == nil || !strings.Contains(err.Error(), "download") {
		t.Fatalf("not built in, not downloaded: %v", err)
	}
	if GeoInfo().Full {
		t.Fatal("nothing downloaded yet")
	}

	os.MkdirAll(filepath.Join(data, "geo"), 0o700)
	os.WriteFile(filepath.Join(data, "geo", "geoip-full.dat"), geoFile(t, "PRIVATE", "IR", "US", "CN", "RU"), 0o600)
	if !GeoInfo().Full {
		t.Fatal("downloaded")
	}
	for _, c := range []string{"us", "!us"} {
		ips, err := geoIPMatch(c)
		if err != nil || !slices.Equal(ips, []string{"ext:../data/geo/geoip-full.dat:" + c}) {
			t.Fatalf("%s: %v %v", c, ips, err)
		}
		// Xray finds it and loads the rule
		cfg := `{"outbounds":[{"protocol":"freedom","tag":"direct"}],"routing":{"rules":[{"ip":["` + ips[0] + `"],"outboundTag":"direct"}]}}`
		if _, err := xcore.LoadConfig("json", bytes.NewReader([]byte(cfg))); err != nil {
			t.Fatalf("%s: Xray: %v", c, err)
		}
	}
	if _, err := geoIPMatch("xx"); err == nil {
		t.Fatal("a country in neither list would fail the whole connection")
	}
	if _, err := geoIPMatch("../x"); err == nil {
		t.Fatal("not a country")
	}
	if err := DeleteFullGeoIP(); err != nil || GeoInfo().Full {
		t.Fatal(err)
	}
}

func TestRuleSkipsMissingCountry(t *testing.T) {
	setGeo(t)
	if _, err := ruleFor(Rule{Match: "geoip", Value: "de", Action: "direct"}, J{}, nil); err == nil {
		t.Fatal("a country that is not here should leave the rule out")
	}
	r, err := ruleFor(Rule{Match: "geoip", Value: "ru", Action: "direct"}, J{}, nil)
	if err != nil || !slices.Equal(r["ip"].([]string), []string{"geoip:ru"}) {
		t.Fatalf("%v %v", r, err)
	}
}
