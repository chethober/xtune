package protocols

import (
	"encoding/hex"
	"fmt"
	"net/url"

	"github.com/chethober/xtune/pkg/state"
)

const (
	MTProtoFrameLen = 64
	MTProtoKeyLen   = 16 // the proxy secret
)

func MTProtoKey(u state.User) []byte { return secret(u, "mtproto")[:MTProtoKeyLen] }

// MTProtoSecret is the secret Telegram apps take: the "dd" prefix (padded-intermediate transport,
// no fake TLS) plus the user's 16-byte key in hex.
func MTProtoSecret(u state.User) string { return "dd" + hex.EncodeToString(MTProtoKey(u)) }

// MTProtoLink opens the proxy in Telegram and doubles as the page's QR code.
func MTProtoLink(st *state.State, e state.Entry, u state.User) string {
	q := url.Values{"server": {st.Addr()}, "port": {fmt.Sprint(e.Port)}, "secret": {MTProtoSecret(u)}}
	return "https://t.me/proxy?" + q.Encode()
}

// MTProtoInfo is what to type into Telegram's proxy settings by hand.
func MTProtoInfo(st *state.State, e state.Entry, u state.User) string {
	return fmt.Sprintf("%s for %s\n\nTelegram > Settings > Data and Storage > Proxy > Add proxy > MTProto\n\n"+
		"Server: %s\nPort: %d\nSecret: %s\n\nOr open: %s\n",
		e.ID, u.Name, st.Addr(), e.Port, MTProtoSecret(u), MTProtoLink(st, e, u))
}
