**Fixes**
- **No more crash when reconnecting with Fake DNS on (Android).** Reconnecting, switching config, resuming after a
  pause or moving to another network could close the app. The old connection kept reading the VPN after it stopped
  and passed the new connection's DNS requests to its own Fake DNS, which was already shut down. Stopping a
  connection now shuts down its part of the VPN first.
- **TUN mode reconnects on Linux and Windows.** Disconnecting left the old TUN interface up, so connecting again in
  TUN mode could fail with "device or resource busy". The interface is now removed when you disconnect.

Updating: install over 1.6.0 or earlier; your subscriptions, settings and servers stay.
