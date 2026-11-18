**Fixes**
- **No more crashes when connecting with a big subscription.** The app handed the whole subscription to Android's VPN
  service in one message, and Android refuses messages that large (from about 300 configs on). It now keeps the
  connection in a file the service reads.
- **Fewer crashes in the background (Android 12 and later).** Auto-connect on Wi-Fi or at boot, the Quick Settings
  tile, the widget and the end of a 15-minute pause no longer close the app when Android refuses to start a VPN from
  the background. You get a "Tap to connect" notification instead. With auto-connect on, Settings offers to exempt
  xtune from battery optimization, so Android lets it connect by itself.
- Connecting a second time while the first is still answering, a pause ending while connecting, and "Only these
  apps" with none of them installed any more no longer crash or loop.

**New**
- **Community VPNs.** Configs and Home's subscription list have **Community VPNs**: free VPNs people share with
  everyone, in the app and in @chthvpnbot. Join one and it becomes your subscription. On an xtune server you get your
  own account, and nobody learns who you are.
- **Share your server.** Manage → **Share** lists your server in the community with the limits you choose (members,
  GB, days, devices). Everyone who joins gets their own user, so you can remove one without touching the others.
  Pause or stop sharing from the same page.
- **Crash log.** Settings → Diagnostics → **Crash log** shows what closed the app and the errors it got past, with
  the secrets taken out, ready to share. The diagnostics bundle includes it. **Log level** sets how much the
  connection log keeps.
- **The best configs for this time of day.** Filtering often changes in the evening, so xtune servers now keep
  their rankings by time of day too, and the app asks for the configs that work now. Subscriptions from other
  servers get the same from your own checks on the device.
- **Fix one blocked config.** Manage marks the configs that look blocked; tap one to give it a new SNI, path or port
  in place, without moving the server's IP or tuning again.
- **All servers at a glance.** With more than one server, Manage → **All servers** shows each one's users online,
  traffic today and blocked configs. The server widget cycles through them with its arrow.

Updating: install over 1.5.0 or earlier; your subscriptions, settings and servers stay. The new Manage features need
xtune 1.6.0 on the server (Manage → Update).
