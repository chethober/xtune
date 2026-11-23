**A release candidate for testing.** The app does not offer it as an update: install it by hand. It needs xtune
1.7.0-rc on the server for the new parts; with an older server they stay hidden.

**New**
- **My configs, in Check.** On an xtune subscription, Check shows your configs as the server gives them to you: put
  the ones you like first, hide the ones you do not want, and choose whether your own results on each network
  order them there. Your choices are kept on the server, for all your devices. Whatever your admin set is shown
  and cannot be changed.
- **Tune my configs, in Check.** When your admin allows it, the app tries variants of your configs from where you
  are (TLS fingerprint, fragmentation ranges, XHTTP mode, with your own account) and the server keeps the best
  settings of each for you. It takes a minute or two and can run once every 10 minutes.
- **A user's tuning, in Manage.** Users → a user → Tuning: turn configs off for that user (the server refuses them
  there too), pin configs first, set a config's client settings, choose a preset, let the user tune their own
  configs, and choose what they may change themselves.

Updating: install over 1.6.1 or earlier; your subscriptions, settings and servers stay.
