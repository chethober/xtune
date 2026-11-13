package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime/debug"
)

// Call is the whole API the app uses, the same on every platform: Android calls it through the
// gomobile binding, desktops through xtune-core's local HTTP server. args and the reply are JSON;
// the reply is {"ok":true,"result":...} or {"ok":false,"error":"..."}.
func Call(method string, args []byte) (reply []byte) {
	defer func() {
		if r := recover(); r != nil {
			reply = fail(fmt.Errorf("internal error in %s: %v\n%s", method, r, debug.Stack()))
		}
	}()
	h, ok := handlers[method]
	if !ok {
		return fail(fmt.Errorf("unknown method %q", method))
	}
	if len(args) == 0 {
		args = []byte("{}")
	}
	res, err := h(args)
	if err != nil {
		return fail(err)
	}
	b, err := json.Marshal(struct {
		Ok     bool `json:"ok"`
		Result any  `json:"result"`
	}{true, res})
	if err != nil {
		return fail(err)
	}
	return b
}

func fail(err error) []byte {
	b, _ := json.Marshal(struct {
		Ok    bool   `json:"ok"`
		Error string `json:"error"`
	}{false, err.Error()})
	return b
}

func decode[T any](args []byte) (T, error) {
	var v T
	if err := json.Unmarshal(args, &v); err != nil {
		return v, fmt.Errorf("bad arguments: %w", err)
	}
	return v, nil
}

type handler func(args []byte) (any, error)

var handlers = map[string]handler{
	"version": func([]byte) (any, error) { return map[string]string{"version": Version}, nil },

	// setup: where the geo files are (Android copies them out of the APK)
	"setup": func(a []byte) (any, error) {
		in, err := decode[struct {
			AssetDir string `json:"assetDir"`
			DataDir  string `json:"dataDir"`
		}](a)
		if err != nil {
			return nil, err
		}
		if in.AssetDir != "" {
			os.Setenv("xray.location.asset", in.AssetDir)
		}
		if in.DataDir != "" {
			SetDataDir(in.DataDir)
		}
		return nil, nil
	},

	"network.current": func([]byte) (any, error) { return CurrentNetwork() },

	"profile.normalize": func(a []byte) (any, error) {
		in, err := decode[struct {
			URL string `json:"url"`
		}](a)
		if err != nil {
			return nil, err
		}
		u, err := NormalizeSubURL(in.URL)
		return map[string]string{"url": u}, err
	},
	"profile.import": func(a []byte) (any, error) {
		in, err := decode[struct {
			Text string `json:"text"`
			ASN  string `json:"asn"`
		}](a)
		if err != nil {
			return nil, err
		}
		return Import(in.Text, in.ASN)
	},
	"logs.tail": func(a []byte) (any, error) {
		in, _ := decode[struct {
			Bytes int64 `json:"bytes"`
		}](a)
		return map[string]string{"log": LogTail(in.Bytes)}, nil
	},
	"diag.bundle": func(a []byte) (any, error) {
		in, _ := decode[struct {
			App map[string]any `json:"app"`
		}](a)
		return map[string]string{"text": Diagnostics(in.App)}, nil
	},
	"profile.fetch": func(a []byte) (any, error) {
		in, err := decode[struct {
			URL string `json:"url"`
			ASN string `json:"asn"`
		}](a)
		if err != nil {
			return nil, err
		}
		return FetchProfile(in.URL, in.ASN)
	},

	"vpn.start": func(a []byte) (any, error) {
		in, err := decode[struct {
			Profile *Profile `json:"profile"`
			Options Options  `json:"options"`
		}](a)
		if err != nil {
			return nil, err
		}
		if in.Profile == nil {
			return nil, errors.New("no profile")
		}
		if err := Start(in.Profile, in.Options); err != nil {
			return nil, err
		}
		return CurrentStatus(), nil
	},
	"vpn.stop": func([]byte) (any, error) { Disconnect(); return CurrentStatus(), nil },
	"vpn.profile": func([]byte) (any, error) {
		p, rev := CurrentProfile()
		return map[string]any{"profile": p, "rev": rev}, nil
	},
	"vpn.status": func([]byte) (any, error) { return CurrentStatus(), nil },
	"vpn.config": func(a []byte) (any, error) { // the Xray config a connection would use, for the logs screen
		in, err := decode[struct {
			Profile *Profile `json:"profile"`
			Options Options  `json:"options"`
		}](a)
		if err != nil || in.Profile == nil {
			return nil, errors.New("no profile")
		}
		cs, err := chosen(in.Profile, in.Options)
		if err != nil {
			return nil, err
		}
		return BuildConfig(in.Profile, in.Options, cs, map[string]int{}, nil), nil
	},

	"probe.start": func(a []byte) (any, error) {
		in, err := decode[struct {
			Profile *Profile     `json:"profile"`
			Options ProbeOptions `json:"options"`
		}](a)
		if err != nil {
			return nil, err
		}
		if in.Profile == nil {
			return nil, errors.New("no profile")
		}
		j := StartProbe(in.Profile, in.Options)
		s := j.snapshot()
		return s, nil
	},
	"probe.status": func(a []byte) (any, error) {
		in, err := decode[struct {
			ID string `json:"id"`
		}](a)
		if err != nil {
			return nil, err
		}
		j, ok := GetJob(in.ID)
		if !ok {
			return nil, errors.New("no such check")
		}
		return j, nil
	},
	"probe.stop": func(a []byte) (any, error) {
		in, err := decode[struct {
			ID string `json:"id"`
		}](a)
		if err != nil {
			return nil, err
		}
		StopJob(in.ID)
		return nil, nil
	},
	"report.send": func(a []byte) (any, error) {
		in, err := decode[struct {
			URL     string   `json:"url"`
			Network Network  `json:"network"`
			Results []Result `json:"results"`
		}](a)
		if err != nil {
			return nil, err
		}
		return nil, SendReport(in.URL, in.Network, in.Results)
	},

	"manage.connect": func(a []byte) (any, error) {
		c, err := decode[ServerConn](a)
		if err != nil {
			return nil, err
		}
		return ConnectServer(c)
	},
	"manage.disconnect": func(a []byte) (any, error) {
		c, err := decode[ServerConn](a)
		if err != nil {
			return nil, err
		}
		DisconnectServer(c)
		return nil, nil
	},
	"manage.overview": func(a []byte) (any, error) {
		c, err := decode[ServerConn](a)
		if err != nil {
			return nil, err
		}
		return ServerOverview(c)
	},
	"manage.users": func(a []byte) (any, error) {
		c, err := decode[ServerConn](a)
		if err != nil {
			return nil, err
		}
		return Users(c)
	},
	"manage.user": func(a []byte) (any, error) {
		in, err := decode[struct {
			Server ServerConn `json:"server"`
			UserAction
		}](a)
		if err != nil {
			return nil, err
		}
		out, err := DoUser(in.Server, in.UserAction)
		if err != nil {
			return nil, err
		}
		if json.Valid([]byte(out)) {
			return json.RawMessage(out), nil
		}
		return map[string]string{"output": out}, nil
	},
	"manage.xtune": func(a []byte) (any, error) {
		in, err := decode[struct {
			Server ServerConn `json:"server"`
			Args   []string   `json:"args"`
		}](a)
		if err != nil {
			return nil, err
		}
		out, err := Xtune(in.Server, in.Args)
		if err != nil {
			return nil, fmt.Errorf("%v\n%s", err, out)
		}
		return map[string]string{"output": out}, nil
	},
	"manage.logs": func(a []byte) (any, error) {
		in, err := decode[struct {
			Server ServerConn `json:"server"`
			Unit   string     `json:"unit"`
			Lines  int        `json:"lines"`
		}](a)
		if err != nil {
			return nil, err
		}
		out, err := Logs(in.Server, in.Unit, in.Lines)
		if err != nil {
			return nil, err
		}
		return map[string]string{"output": out}, nil
	},
}
