**Manage your servers**
- **Tune from your phone**: the server starts test configs, the app measures every one from the network you are on,
  you see what got through and how fast, and the best are deployed. Users and their links are kept. Choose the
  protocols to add or keep: **SSH tunnel**, **DNS tunnel**, **MTProto** (Telegram), **AmneziaWG**, Trojan and the
  Cloudflare Worker relay, with plain Xray configs always tuned. Smart (a few minutes) or deep.
- **Update** xtune and Xray on the server from the app.
- Needs xtune 1.1.0 on the server: open Manage → Update → xtune once.
- OpenVPN and IKEv2 run in other apps, so the phone cannot measure them and a tune from the app drops them (the page
  warns you); tune from a computer to keep them.

**Fixes**
- **Speeds and usage in VPN mode**: the Home screen's live speeds and the usage history stayed at zero in VPN mode
  (always on Android). Traffic is now counted on the configs themselves, in every mode. Sites that go direct are not
  counted.
- **Tab bar**: the highlight no longer slides across when you come back to the app, and no longer shows on two tabs
  at once.
- **Settings**: the accent colour row lines up with the rows around it.
- **Check**: MTProto configs are checked too (whether their port answers) instead of skipped.

**Look**
- New dropdowns and menus in the app's glass style: sorting configs, rule presets, rule types, the TLS fingerprint
  and the subscription menu.

Updating: install over 1.1.x or 1.0.x; your subscriptions, settings and servers stay. Android: the `arm64-v8a` APK.
