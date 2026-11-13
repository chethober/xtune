**Connection**
- **Notification controls** (Android): the config in use and live speeds, with Disconnect, **Next config** and
  **Pause 15 min** (the tunnel closes and reconnects by itself; "Paused until" shows meanwhile).
- **How Auto picks**: lowest ping (as before), **calls and games** (checks every 15 s and moves sooner), or
  **steadiest** (for downloads and streams).
- **Send sites through a chosen config**: a rule's new action, "via", sends those sites through one config
  whatever Auto picks. On Linux and Windows, rules can match **apps** too (telegram.exe, firefox).
- **AmneziaWG `.conf` files** import as configs of their own, like WireGuard ones.
- **Data saver** (Android): on mobile data, checks skip their download tests.
- **Hotel and airport Wi-Fi** (Android): auto-connect waits until you have signed in, and Home offers the sign-in
  page.

**Your servers**
- **Users**: add several at once (prefix-01, prefix-02, …) and share all their links; send a link straight to a
  Telegram chat; filter by online, expiring and over 90% of quota; each user's **traffic per day**; their
  **devices**, with **Kick** (blocked for an hour) and Unblock.
- **Domains and CDN**: save a Cloudflare token on the server and tune with your domain; the server gets a domain
  of its own and a CDN config to fall back on when its IP is blocked.
- **Server widget** (Android): who is online, today's traffic and whether its configs answer, as the app last saw
  them.
- Needs xtune 1.2.0 on the server for Kick, the per-user charts and the Cloudflare token: Manage → Update →
  xtune.

**Privacy and safety**
- **App lock**: a PIN (and the fingerprint or face, where the device has one) for the whole app, or only the
  Manage tab with your servers' logins. It locks again after a minute away.
- **Leak test**: which address sites see, which DNS servers answer, and whether IPv6 is blocked.
- **IPv6 leak fixed** (Android): with IPv6 off, apps' IPv6 traffic went around the VPN; it now comes into the
  tunnel and is blocked there.
- **Backup**: subscriptions, settings and servers (logins included) in one file sealed with a passphrase, to move
  to a new phone; restore it from the first-run guide or Settings.

**App**
- **First-run guide**: what xtune does, scan or paste a subscription, the VPN permission. Home's empty state has
  a Scan QR button too.
- **Connection timeline**: when it connected, switched configs (and why), moved networks or paused.
- **Persian**: Persian digits and the Persian (Jalali) calendar throughout.
- **Wide screens**: Configs shows a config's details beside the list; Manage shows the users beside the
  dashboard.
- Warnings on Home when a subscription is nearly out of data or ends within three days.

Updating: install over 1.2.x or earlier; your subscriptions, settings and servers stay. Android: the `arm64-v8a`
APK.
