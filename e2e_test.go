package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestCheckEndToEnd runs a real local Xray as the "server" and measures it with xtune check.
// It needs a local xray and internet access, so it only runs with XTUNE_E2E=1.
func TestCheckEndToEnd(t *testing.T) {
	if os.Getenv("XTUNE_E2E") == "" {
		t.Skip("set XTUNE_E2E=1 to run (needs a local xray and internet access)")
	}
	bin := localXray(t)
	fs := newFake()
	r := &Server{sh: fs, Conn: Conn{Host: "127.0.0.1"}}
	st, err := NewState("127.0.0.1", bin)
	if err != nil {
		t.Fatal(err)
	}
	ports, _ := freePorts(3)
	st.Entries = []Entry{{ID: "venc-tcp", Kind: "vlessenc-tcp", Port: ports[0]}, {ID: "ss2022", Kind: "ss2022", Port: ports[1]},
		// one-port "range": the client still runs its port-hopping code against the real server
		{ID: "hy2-hop", Kind: "hysteria2", Port: ports[2], Hop: itoa(ports[2]) + "-" + itoa(ports[2])}}
	st.Users = []User{NewUser("ann")}
	if err := Apply(r, st); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureCheckUser(r); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "hy.crt"), []byte(st.CertPEM), 0o600)
	os.WriteFile(filepath.Join(dir, "hy.key"), []byte(st.KeyPEM), 0o600)
	var cfg J
	json.Unmarshal([]byte(fs.files[remoteDir+"/config.json"]), &cfg)
	inbounds := []J{}
	for _, ib := range cfg["inbounds"].([]any) {
		inbounds = append(inbounds, ib.(map[string]any))
	}
	cfg["inbounds"] = inbounds
	testCerts(cfg, dir)
	delete(cfg, "api") // its port may be taken here
	srvCfg := filepath.Join(dir, "server.json")
	writeJSON(srvCfg, cfg)
	srv := exec.Command(bin, "run", "-c", srvCfg)
	srv.Env = append(os.Environ(), "XRAY_LOCATION_ASSET="+filepath.Dir(bin))
	out, _ := os.Create(filepath.Join(dir, "server.log"))
	srv.Stdout, srv.Stderr = out, out
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { srv.Process.Kill(); srv.Wait() }()
	time.Sleep(time.Second)

	res, _, err := Check(r, bin, 3, 4, dir, func(s string) { t.Log(s) })
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range res {
		if c.Status == "blocked" || c.Status == "flaky" || c.Down == 0 {
			log, _ := os.ReadFile(filepath.Join(dir, "xray.log"))
			slog, _ := os.ReadFile(filepath.Join(dir, "server.log"))
			t.Errorf("%s: %s (client log: %s) (server log: %s)", c.ID, c.Status, tail(string(log), 400), tail(string(slog), 400))
		}
	}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

// TestSplitEndToEnd sends XHTTP uploads through the "CDN" (localhost, trusted for the test) and
// downloads straight to the server, both into one real inbound, as a split CDN entry does.
func TestSplitEndToEnd(t *testing.T) {
	if os.Getenv("XTUNE_E2E") == "" {
		t.Skip("set XTUNE_E2E=1 to run (needs a local xray and internet access)")
	}
	bin := localXray(t)
	fs := newFake()
	r := &Server{sh: fs, Conn: Conn{Host: "127.0.0.1"}}
	st, err := NewState("127.0.0.1", bin)
	if err != nil {
		t.Fatal(err)
	}
	st.CertSNI, st.CDNDomain = "localhost", "localhost"
	caPEM := testCA(t, st) // the server certificate, signed by a CA the client trusts

	ports, _ := freePorts(2)
	st.Entries = []Entry{{ID: "cdn-xhttp", Kind: "cdn-xhttp", Port: ports[0], Path: "/a", Mode: "packet-up"},
		{ID: "cdn-split", Kind: "cdn-xhttp", Port: ports[1], Path: "/b", Mode: "packet-up", Split: "cdn-up"}}
	st.Users = []User{NewUser("ann")}
	if err := Apply(r, st); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "hy.crt"), []byte(st.CertPEM), 0o600)
	os.WriteFile(filepath.Join(dir, "hy.key"), []byte(st.KeyPEM), 0o600)
	os.WriteFile(filepath.Join(dir, "ca.crt"), caPEM, 0o600)
	var cfg J
	json.Unmarshal([]byte(fs.files[remoteDir+"/config.json"]), &cfg)
	inbounds := []J{}
	for _, ib := range cfg["inbounds"].([]any) {
		inbounds = append(inbounds, ib.(map[string]any))
	}
	cfg["inbounds"] = inbounds
	testCerts(cfg, dir)
	delete(cfg, "api")
	cfg["log"] = J{"loglevel": "info"}
	srvCfg := filepath.Join(dir, "server.json")
	writeJSON(srvCfg, cfg)
	srv := exec.Command(bin, "run", "-c", srvCfg)
	srv.Env = append(os.Environ(), "XRAY_LOCATION_ASSET="+filepath.Dir(bin))
	out, _ := os.Create(filepath.Join(dir, "server.log"))
	srv.Stdout, srv.Stderr = out, out
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { srv.Process.Kill(); srv.Wait() }()
	time.Sleep(time.Second)
	// the client: the generated outbounds, with the "CDN" leg trusting the test CA (Xray has its
	// own root store, so the CA is added the Xray way)
	cport, _ := freePorts(len(st.Entries))
	ccfg := J{"log": J{"loglevel": "warning"}, "inbounds": []J{}, "outbounds": []J{}, "routing": J{"rules": []J{}}}
	for i, e := range st.Entries {
		ob := st.Outbound(e, st.Users[0], e.Knobs(), "out"+itoa(i))
		ob["streamSettings"].(J)["tlsSettings"].(J)["certificates"] = []J{{"usage": "verify", "certificateFile": filepath.Join(dir, "ca.crt")}}
		ccfg["outbounds"] = append(ccfg["outbounds"].([]J), ob)
		ccfg["inbounds"] = append(ccfg["inbounds"].([]J), J{"tag": "in" + itoa(i), "listen": "127.0.0.1", "port": cport[i], "protocol": "socks"})
		ccfg["routing"].(J)["rules"] = append(ccfg["routing"].(J)["rules"].([]J), J{"inboundTag": []string{"in" + itoa(i)}, "outboundTag": "out" + itoa(i)})
	}
	cliCfg := filepath.Join(dir, "client.json")
	writeJSON(cliCfg, ccfg)
	cli := exec.Command(bin, "run", "-c", cliCfg)
	clog, _ := os.Create(filepath.Join(dir, "client.log"))
	cli.Stdout, cli.Stderr = clog, clog
	if err := cli.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cli.Process.Kill(); cli.Wait() }()
	time.Sleep(time.Second)
	for i, e := range st.Entries {
		v := &Variant{port: cport[i], Label: e.ID}
		probe(v, 3)
		tp := sustained(v, 4, func(s string) { t.Log(s) })
		t.Logf("%s (split=%q): ok=%d/3 down=%.2f Mbps", e.ID, e.Split, v.Ok, tp.Avg)
		if v.Ok == 0 || tp.Avg == 0 {
			c, _ := os.ReadFile(filepath.Join(dir, "client.log"))
			sl, _ := os.ReadFile(filepath.Join(dir, "server.log"))
			t.Errorf("%s failed (client log: %s) (server log: %s)", e.ID, tail(string(c), 1500), tail(string(sl), 600))
		}
	}
}

// testCA gives st a certificate for localhost signed by a new CA and returns the CA's PEM.
func testCA(t *testing.T, st *State) []byte {
	t.Helper()
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "xtune test CA"}, IsCA: true,
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ = x509.ParseCertificate(caDER)
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "localhost"}, DNSNames: []string{"localhost"},
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, leaf, ca, &k.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	kd, _ := x509.MarshalECPrivateKey(k)
	sum := sha256.Sum256(der)
	st.CertHash = hex.EncodeToString(sum[:])
	st.CertPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	st.KeyPEM = string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kd}))
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
}
