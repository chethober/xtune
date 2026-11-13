**Connection**
- **Auto-connect**: when the app opens, when the phone starts (Android), and on Wi-Fi networks that are not in your
  trusted list (Android; reading Wi-Fi names needs the location permission).
- **Smart reconnect**: move to another network and xtune fetches that network's ranking and reconnects with its best
  configs.
- **Kill switch**: Android's always-on VPN with "Block connections without VPN" (a shortcut in Settings); on Linux, a
  kill switch of its own. Not on Windows yet.
- **Tweaks**, for every config or one: TLS fragmentation, uTLS fingerprint, Mux, MTU.

**Routing and DNS**
- **Your own routing rules** (domains, keywords, site lists, IPs, countries, ports: direct, proxy or block), with
  presets for Iran, China and Russia, trackers, and streaming.
- **DNS**: DoH, TCP or UDP resolver; a separate resolver for domestic sites; fake DNS.
- **IPv6** routed or blocked (no leaks), **QUIC** blocking, and lists that go direct: your domains, push
  notifications, the local network.

**Configs**
- **Import anything**: other providers' subscriptions, and vless://, vmess://, trojan://, ss://, hy2:// links or
  WireGuard configs (pasted or scanned).
- **Subscriptions update themselves** on a schedule you choose.
- **Star, hide and rename** configs; choose **which configs Auto uses**, in what order.
- **Speed test** a single config from its sheet.

**App**
- **Quick Settings tile** and a **home-screen widget** on Android.
- **Themes**: dark, pure black and light (or follow the system), with four accent colours.
- **Usage**: traffic per day and per config, for up to 90 days.
- **Diagnostics**: the connection log in the app, and a report to share with every key, ID and link removed.

Updating: install over 1.0.x; your subscriptions and settings stay. Android: the `arm64-v8a` APK.
