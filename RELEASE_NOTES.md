**New**
- **Route map** on Home: a map of where your traffic goes, from you through each relay to the server, with the delay
  of every hop. The route in use is drawn in your accent color with traffic moving along it; the others are faint,
  and a route that does not work is red. Below the map, the best routes: tap one to use it. The first hop's delay is
  measured from your device; the rest is the config's delay less that, so it is marked ≈. Where a config goes comes
  from the flags in its name (🇮🇷→🇳🇱).
- **Close to tray** (Linux and Windows, Settings → Connection, on by default): closing the window keeps xtune running
  and connected in the tray, so a stray click no longer drops the connection. The tray icon opens the window, and its
  menu connects, disconnects or quits. Opening xtune again brings back the running window instead of a second copy.
  On GNOME the tray icon needs the AppIndicator extension (Ubuntu has it on).

**Fixes**
- **Theme and accent** now change at once. Before, the background and the tab bar kept the old look until you
  switched tabs.

**Changed**
- The tab bar is at the bottom on every screen size; wide windows no longer have a sidebar.

Updating: install over 1.3.1 or earlier; your subscriptions, settings and servers stay. Android: the `arm64-v8a`
APK.
