package main

import (
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"os/exec"
	"regexp"
	"time"

	"github.com/chethober/xtune/internal/state"
)

func randHex(n int) string { b := make([]byte, n); rand.Read(b); return hex.EncodeToString(b) }

// NewState generates every secret. REALITY keys and the Hysteria2 certificate are made here;
// the VLESS Encryption pair comes from the local xray binary so its format always matches.
func NewState(host, xrayBin string) (*state.State, error) {
	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	st := &state.State{Host: host, ShortID: randHex(4), SSKey: state.NewSSKey(), CertSNI: "www.bing.com",
		RealityPriv: base64.RawURLEncoding.EncodeToString(priv.Bytes()),
		RealityPub:  base64.RawURLEncoding.EncodeToString(priv.PublicKey().Bytes())}
	out, err := exec.Command(xrayBin, "vlessenc").Output()
	if err != nil {
		return nil, fmt.Errorf("xray vlessenc: %w (local xray too old?)", err)
	}
	// the X25519-authenticated pair is printed first
	if m := regexp.MustCompile(`"decryption":\s*"([^"]+)"`).FindSubmatch(out); m != nil {
		st.VencDec = string(m[1])
	}
	if m := regexp.MustCompile(`"encryption":\s*"([^"]+)"`).FindSubmatch(out); m != nil {
		st.VencEnc = string(m[1])
	}
	if st.VencDec == "" || st.VencEnc == "" {
		return nil, fmt.Errorf("could not parse `xray vlessenc` output")
	}
	if err := makeCert(st); err != nil {
		return nil, err
	}
	if _, err := ensureKeys(st); err != nil {
		return nil, err
	}
	return st, nil
}

func makeCert(st *state.State) error {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	tpl := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: st.CertSNI},
		DNSNames: []string{st.CertSNI}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(10, 0, 0),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &k.PublicKey, k)
	if err != nil {
		return err
	}
	kd, err := x509.MarshalECPrivateKey(k)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(der)
	st.CertHash = hex.EncodeToString(sum[:])
	st.CertPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	st.KeyPEM = string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kd}))
	return nil
}

// ---------------------------------------------------------------- server config

type J = map[string]any

// ---------------------------------------------------------------- client side

func orStr(s, d string) string {
	if s == "" {
		return d
	}
	return s
}
