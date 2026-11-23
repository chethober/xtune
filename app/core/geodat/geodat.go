// Package geodat reads Xray's geoip.dat and geosite.dat without decoding all of them: both are a
// list of entries (field 1), each starting with its code (field 1). A full geoip.dat is 24 MB, so
// the app ships a trimmed one and reads other countries from a downloaded full file on demand.
package geodat

import (
	"errors"
	"fmt"
	"net/netip"
	"strings"

	"google.golang.org/protobuf/encoding/protowire"
)

// entries calls fn with each entry's code (upper case) and its encoded bytes.
func entries(b []byte, fn func(code string, raw []byte) bool) error {
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return protowire.ParseError(n)
		}
		b = b[n:]
		if num != 1 || typ != protowire.BytesType {
			n = protowire.ConsumeFieldValue(num, typ, b)
			if n < 0 {
				return protowire.ParseError(n)
			}
			b = b[n:]
			continue
		}
		raw, n := protowire.ConsumeBytes(b)
		if n < 0 {
			return protowire.ParseError(n)
		}
		b = b[n:]
		if !fn(strings.ToUpper(code(raw)), raw) {
			return nil
		}
	}
	return nil
}

// code is an entry's field 1.
func code(raw []byte) string {
	for len(raw) > 0 {
		num, typ, n := protowire.ConsumeTag(raw)
		if n < 0 {
			return ""
		}
		raw = raw[n:]
		if num == 1 && typ == protowire.BytesType {
			v, _ := protowire.ConsumeBytes(raw)
			return string(v)
		}
		if n = protowire.ConsumeFieldValue(num, typ, raw); n < 0 {
			return ""
		}
		raw = raw[n:]
	}
	return ""
}

// Codes lists the file's codes, upper case.
func Codes(b []byte) ([]string, error) {
	var out []string
	err := entries(b, func(c string, _ []byte) bool { out = append(out, c); return true })
	return out, err
}

// Trim keeps the entries whose code is in keep (any case); it fails if one of them is missing.
func Trim(b []byte, keep []string) ([]byte, error) {
	want := map[string]bool{}
	for _, k := range keep {
		want[strings.ToUpper(k)] = true
	}
	var out []byte
	err := entries(b, func(c string, raw []byte) bool {
		if want[c] {
			out = protowire.AppendTag(out, 1, protowire.BytesType)
			out = protowire.AppendBytes(out, raw)
			delete(want, c)
		}
		return true
	})
	if err != nil {
		return nil, err
	}
	for k := range want {
		return nil, fmt.Errorf("no %q in the file", strings.ToLower(k))
	}
	return out, nil
}

// CIDRs are a geoip.dat country's ranges, as text ("1.2.3.0/24").
func CIDRs(b []byte, country string) ([]string, error) {
	country = strings.ToUpper(country)
	var entry []byte
	if err := entries(b, func(c string, raw []byte) bool {
		if c == country {
			entry = raw
			return false
		}
		return true
	}); err != nil {
		return nil, err
	}
	if entry == nil {
		return nil, fmt.Errorf("no country %q", strings.ToLower(country))
	}
	var out []string
	for len(entry) > 0 {
		num, typ, n := protowire.ConsumeTag(entry)
		if n < 0 {
			return nil, protowire.ParseError(n)
		}
		entry = entry[n:]
		if num == 3 && typ == protowire.VarintType { // reverse_match: not something a country has
			return nil, errors.New("inverted country lists are not supported")
		}
		if num != 2 || typ != protowire.BytesType {
			if n = protowire.ConsumeFieldValue(num, typ, entry); n < 0 {
				return nil, protowire.ParseError(n)
			}
			entry = entry[n:]
			continue
		}
		cidr, n := protowire.ConsumeBytes(entry)
		if n < 0 {
			return nil, protowire.ParseError(n)
		}
		entry = entry[n:]
		p, err := prefix(cidr)
		if err != nil {
			return nil, err
		}
		out = append(out, p.String())
	}
	return out, nil
}

// prefix decodes a CIDR message: ip (field 1, 4 or 16 bytes) and prefix (field 2).
func prefix(b []byte) (netip.Prefix, error) {
	var ip []byte
	var bits uint64
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return netip.Prefix{}, protowire.ParseError(n)
		}
		b = b[n:]
		switch {
		case num == 1 && typ == protowire.BytesType:
			ip, n = protowire.ConsumeBytes(b)
		case num == 2 && typ == protowire.VarintType:
			bits, n = protowire.ConsumeVarint(b)
		default:
			n = protowire.ConsumeFieldValue(num, typ, b)
		}
		if n < 0 {
			return netip.Prefix{}, protowire.ParseError(n)
		}
		b = b[n:]
	}
	a, ok := netip.AddrFromSlice(ip)
	if !ok {
		return netip.Prefix{}, fmt.Errorf("bad address of %d bytes", len(ip))
	}
	return a.Prefix(int(bits))
}
