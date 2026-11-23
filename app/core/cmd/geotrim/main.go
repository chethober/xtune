// geotrim keeps only some codes of a geoip.dat or geosite.dat:
//
//	geotrim -in geoip.dat -out geoip.dat private,ir,cn,ru
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/chethober/xtune/app/core/geodat"
)

func main() {
	in := flag.String("in", "", "the full file")
	out := flag.String("out", "", "where to write the trimmed file (may be -in)")
	flag.Parse()
	if *in == "" || *out == "" || flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: geotrim -in FILE -out FILE code,code,…")
		os.Exit(2)
	}
	b, err := os.ReadFile(*in)
	if err == nil {
		b, err = geodat.Trim(b, strings.Split(flag.Arg(0), ","))
	}
	if err == nil {
		err = os.WriteFile(*out, b, 0o644)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "geotrim:", err)
		os.Exit(1)
	}
}
