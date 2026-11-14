**New**
- **Refresh and remove subscriptions from Home.** A refresh button beside the subscription fetches it again, and the
  subscription list (tap its name) has a refresh and a remove button on each one. Removing the one you are connected
  through disconnects first and switches to the next.
- **Wide windows use the room.** Every tab now has the same frame: one column on a phone, and on a wide window a
  column with a pane beside it. Home puts the route map and usage beside the connect button, Check puts its summary
  beside the results, Settings shows its sections in two columns, and Configs and Manage keep their details and users
  beside the list. Pages you open from them (users, tune, logs) keep the same width.

**Fixes**
- **Opening xtune from the tray** (Linux) no longer shows a black window until you resize it. Launching xtune again
  while it is in the tray brings it back the same way.
- **Tune** has a back button. Every page you open now gets one, so none can be missing it again.

**Changed**
- xtune and the app are one repository, [chethober/xtune](https://github.com/chethober/xtune). Releases and in-app
  updates come from there; the app and the CLI share a version, which is why this one follows 1.4 with 1.5.

Updating: install over 1.4.0 or earlier; your subscriptions, settings and servers stay. Android: the `arm64-v8a`
APK.
