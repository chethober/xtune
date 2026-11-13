import 'dart:async';
import 'dart:io';
import 'dart:math';

import 'package:flutter/foundation.dart';
import 'package:path_provider/path_provider.dart';

import '../core/core_client.dart';
import 'models.dart';
import 'store.dart';

/// Everything the screens show and do.
class AppState extends ChangeNotifier {
  AppState(this.core);
  final CoreClient core;
  late Prefs _prefs;

  List<Sub> subs = [];
  String? activeSubId;
  Settings settings = Settings();
  List<ServerEntry> servers = [];
  String? activeServerId;

  Map<String, dynamic> status = {};
  Map<String, dynamic>? network; // {asn, name}
  bool busy = false;
  String? error;
  Timer? _poll;

  // the on-device check
  Map<String, dynamic>? check; // {id, done, results}
  String? checkSubId;
  String? reportMessage;
  Timer? _checkPoll;

  /// Single-config speed tests, by config ID: a result (state, warmMs, downMbps…) or testing.
  final Map<String, Map<String, dynamic>> speed = {};

  /// Traffic this device moved: day (yyyy-mm-dd) -> subscription ID -> config ID -> bytes.
  Map<String, Map<String, Map<String, int>>> history = {};
  int _lastUp = 0, _lastDown = 0, _lastSince = 0, _profileRev = 0;
  bool _historyDirty = false;
  Timer? _maintain;

  Sub? get sub => subs.where((s) => s.id == activeSubId).firstOrNull ?? subs.firstOrNull;
  ServerEntry? get server => servers.where((s) => s.id == activeServerId).firstOrNull ?? servers.firstOrNull;
  bool get connected => status['running'] == true;

  static String newId() => '${DateTime.now().millisecondsSinceEpoch}${Random().nextInt(1 << 20)}';

  Future<void> init() async {
    _prefs = await Prefs.open();
    settings = Settings.fromJson((_prefs.getJson('settings') as Map?)?.cast<String, dynamic>());
    subs = ((_prefs.getJson('subs') as List?) ?? []).map((j) => Sub.fromJson(Map<String, dynamic>.from(j as Map))).toList();
    activeSubId = _prefs.getJson('activeSub') as String?;
    servers = ((_prefs.getJson('servers') as List?) ?? []).map((j) => ServerEntry.fromJson(Map<String, dynamic>.from(j as Map))).toList();
    activeServerId = _prefs.getJson('activeServer') as String?;
    history = ((_prefs.getJson('history') as Map?) ?? {}).map(
      (d, subs) => MapEntry(
        d as String,
        (subs as Map).map((k, v) => MapEntry(k as String, (v as Map).map((c, b) => MapEntry(c as String, (b as num).toInt())))),
      ),
    );
    notifyListeners();
    unawaited(_afterStart());
  }

  Future<void> _afterStart() async {
    try {
      final dir = await getApplicationSupportDirectory();
      await core.call('setup', {'dataDir': dir.path});
    } catch (_) {}
    await refreshStatus();
    _lastSince = (status['since'] as num?)?.toInt() ?? 0;
    _lastUp = (status['up'] as num?)?.toInt() ?? 0;
    _lastDown = (status['down'] as num?)?.toInt() ?? 0;
    await refreshNetwork();
    _poll = Timer.periodic(const Duration(seconds: 1), (_) => refreshStatus());
    _maintain = Timer.periodic(const Duration(minutes: 10), (_) => _maintenance());
    if (settings.autoConnectLaunch && !connected && sub != null) unawaited(connect());
    await _maintenance();
  }

  /// Every few minutes: subscriptions due for an update, and the traffic history to disk.
  Future<void> _maintenance() async {
    if (settings.autoUpdateHours > 0) {
      final now = DateTime.now();
      for (final s in List.of(subs)) {
        if (!s.local && now.difference(s.fetched) > Duration(hours: settings.autoUpdateHours)) {
          try {
            await refreshSub(s);
          } catch (_) {}
        }
      }
    }
    await _saveHistory();
  }

  Future<void> _saveHistory() async {
    if (!_historyDirty) return;
    _historyDirty = false;
    final cutoff = _day(DateTime.now().subtract(const Duration(days: 92)));
    history.removeWhere((d, _) => d.compareTo(cutoff) < 0);
    await _prefs.setJson('history', history);
  }

  static String _day(DateTime t) => '${t.year}-${t.month.toString().padLeft(2, '0')}-${t.day.toString().padLeft(2, '0')}';

  @override
  void dispose() {
    _poll?.cancel();
    _checkPoll?.cancel();
    _maintain?.cancel();
    super.dispose();
  }

  Future<void> _save() async {
    await _prefs.setJson('settings', settings.toJson());
    await _prefs.setJson('subs', subs.map((s) => s.toJson()).toList());
    await _prefs.setJson('activeSub', activeSubId);
    await _prefs.setJson('servers', servers.map((s) => s.toJson()).toList());
    await _prefs.setJson('activeServer', activeServerId);
  }

  /// The system switched between light and dark.
  void themeChanged() => notifyListeners();

  Future<void> updateSettings(void Function(Settings s) f) async {
    f(settings);
    notifyListeners();
    await _save();
  }

  // ------------------------------------------------------------ network and subscriptions

  Future<void> refreshNetwork() async {
    if (connected && settings.mode == 'vpn' && !Platform.isAndroid) return; // would see the server's network
    try {
      network = Map<String, dynamic>.from(await core.call('network.current') as Map);
      notifyListeners();
    } catch (_) {}
  }

  String get _asn => (network?['asn'] as String?) ?? '';

  /// Adds what the user pasted or scanned: an xtune subscription, another provider's, or links.
  /// Returns the subscription and how many lines were skipped.
  Future<(Sub, int)> addSub(String text) async {
    final r = Map<String, dynamic>.from(await core.call('profile.import', {'text': text, 'asn': _asn}) as Map);
    final url = r['url'] as String? ?? '';
    final profile = Map<String, dynamic>.from(r['profile'] as Map);
    final existing = url.isEmpty ? null : subs.where((s) => s.url == url).firstOrNull;
    final s = existing ?? Sub(id: newId(), url: url, profile: profile, fetched: DateTime.now());
    s.profile = profile;
    s.fetched = DateTime.now();
    if (existing == null) subs.add(s);
    activeSubId = s.id;
    notifyListeners();
    await _save();
    return (s, (r['skipped'] as num?)?.toInt() ?? 0);
  }

  Future<void> refreshSub(Sub s) async {
    if (s.local) return; // pasted links: nothing to refresh from
    final profile = Map<String, dynamic>.from(await core.call('profile.fetch', {'url': s.url, 'asn': _asn}) as Map);
    s.profile = profile;
    s.fetched = DateTime.now();
    notifyListeners();
    await _save();
  }

  Future<void> removeSub(Sub s) async {
    subs.remove(s);
    if (activeSubId == s.id) activeSubId = subs.firstOrNull?.id;
    notifyListeners();
    await _save();
  }

  Future<void> selectSub(Sub s) async {
    activeSubId = s.id;
    settings.selected = null;
    notifyListeners();
    await _save();
    if (connected) await connect();
  }

  // ------------------------------------------------------------ connection

  Map<String, dynamic> _options() {
    final st = settings, s = sub;
    final configTweaks = <String, dynamic>{};
    s?.meta.forEach((id, m) {
      if (m.tweaks.isNotEmpty) configTweaks[id] = m.tweaks;
    });
    return {
      'mode': st.mode == 'vpn' ? 'tun' : 'proxy',
      'selected': st.selected ?? '',
      'pool': s?.pool ?? [],
      'shareLan': st.shareLan,
      'blockAds': st.blockAds,
      'domestic': st.domestic,
      'includeDnstt': st.includeDnstt,
      'dns': st.dns,
      'domesticDns': st.domesticDns,
      'fakeDns': st.fakeDns,
      'ipv6': st.ipv6,
      'blockQuic': st.blockQuic,
      'proxyLan': st.proxyLan,
      'bypassPush': st.bypassPush,
      'bypassDomains': st.bypassDomains,
      'rules': st.rules.map((r) => r.toJson()).toList(),
      'tweaks': st.tweaks,
      'configTweaks': configTweaks,
      'killSwitch': st.killSwitch,
      'smartUrl': st.smartReconnect && s != null && !s.local ? s.url : '',
      'asn': _asn,
      'socksPort': st.socksPort,
      'httpPort': st.httpPort,
    };
  }

  Future<void> refreshStatus() async {
    try {
      final s = await core.call('vpn.status');
      status = Map<String, dynamic>.from(s as Map);
      _countTraffic();
      final rev = (status['profileRev'] as num?)?.toInt() ?? 0;
      if (rev != _profileRev) {
        _profileRev = rev;
        unawaited(_adoptProfile());
      }
      notifyListeners();
    } catch (_) {}
  }

  /// Smart reconnect swapped the profile (the device moved networks): keep the new one.
  Future<void> _adoptProfile() async {
    try {
      final r = Map<String, dynamic>.from(await core.call('vpn.profile') as Map);
      final p = r['profile'];
      final s = sub;
      if (p is Map && s != null && !s.local) {
        s.profile = Map<String, dynamic>.from(p);
        s.fetched = DateTime.now();
        await refreshNetwork();
        await _save();
        notifyListeners();
      }
    } catch (_) {}
  }

  /// Adds what moved since the last status to today's history, on the config carrying it.
  void _countTraffic() {
    final since = (status['since'] as num?)?.toInt() ?? 0;
    final up = (status['up'] as num?)?.toInt() ?? 0, down = (status['down'] as num?)?.toInt() ?? 0;
    if (status['running'] != true) {
      _lastSince = 0;
      return;
    }
    if (since != _lastSince || up < _lastUp || down < _lastDown) {
      // a new connection: its counters start from zero
      _lastSince = since;
      _lastUp = 0;
      _lastDown = 0;
    }
    final delta = (up - _lastUp) + (down - _lastDown);
    _lastUp = up;
    _lastDown = down;
    final s = sub;
    if (delta <= 0 || s == null) return;
    final cfg = (status['active'] as String?) ?? '?';
    final day = history.putIfAbsent(_day(DateTime.now()), () => {});
    final perSub = day.putIfAbsent(s.id, () => {});
    perSub[cfg] = (perSub[cfg] ?? 0) + delta;
    _historyDirty = true;
  }

  /// Bytes per day for the last [days] days, oldest first; only subscription subId when given.
  List<(DateTime, int)> dailyTraffic(int days, {String? subId}) {
    final now = DateTime.now();
    return [
      for (var i = days - 1; i >= 0; i--)
        () {
          final d = DateTime(now.year, now.month, now.day).subtract(Duration(days: i));
          final day = history[_day(d)] ?? {};
          var sum = 0;
          day.forEach((sid, cfgs) {
            if (subId == null || sid == subId) sum += cfgs.values.fold(0, (a, b) => a + b);
          });
          return (d, sum);
        }(),
    ];
  }

  /// Bytes per config over the last [days] days, largest first.
  List<(String, int)> trafficByConfig(int days, {String? subId}) {
    final cutoff = _day(DateTime.now().subtract(Duration(days: days - 1)));
    final sums = <String, int>{};
    history.forEach((d, subs) {
      if (d.compareTo(cutoff) < 0) return;
      subs.forEach((sid, cfgs) {
        if (subId != null && sid != subId) return;
        cfgs.forEach((c, b) => sums[c] = (sums[c] ?? 0) + b);
      });
    });
    final out = sums.entries.map((e) => (e.key, e.value)).toList()..sort((a, b) => b.$2.compareTo(a.$2));
    return out;
  }

  Future<void> connect() async {
    final s = sub;
    if (s == null) {
      error = 'Add a subscription first';
      notifyListeners();
      return;
    }
    busy = true;
    error = null;
    notifyListeners();
    try {
      await refreshNetwork();
      try {
        await refreshSub(s); // ordered for this network
      } catch (_) {} // offline from here is fine: the saved profile works
      final opts = _options();
      if (Platform.isAndroid && settings.mode == 'vpn') {
        opts['ipv6'] = settings.ipv6;
        await (core as AndroidCore).startVpn(s.profile, opts, settings.perAppMode, settings.apps);
      } else {
        if (settings.mode == 'vpn') {
          await core.ensureElevated();
          opts['routes'] = true;
        } else {
          opts['systemProxy'] = !Platform.isAndroid;
        }
        await core.call('vpn.start', {'profile': s.profile, 'options': opts});
      }
      await refreshStatus();
    } catch (e) {
      error = e.toString();
    } finally {
      busy = false;
      notifyListeners();
    }
  }

  Future<void> disconnect() async {
    busy = true;
    notifyListeners();
    try {
      if (Platform.isAndroid && settings.mode == 'vpn') {
        await (core as AndroidCore).stopVpn();
      }
      await core.call('vpn.stop');
    } catch (e) {
      error = e.toString();
    } finally {
      busy = false;
      await refreshStatus();
    }
  }

  Future<void> toggle() => connected ? disconnect() : connect();

  Future<void> select(String? id) async {
    settings.selected = id;
    notifyListeners();
    await _save();
    if (connected) await connect();
  }

  /// The config carrying traffic now.
  Config? get activeConfig {
    final id = status['active'] as String?;
    return sub?.configs.where((c) => c.id == id).firstOrNull;
  }

  int? delayOf(String id) {
    for (final o in (status['outbounds'] as List?) ?? []) {
      final m = o as Map;
      if (m['id'] == id) return m['alive'] == true ? (m['delayMs'] as num?)?.toInt() : -1;
    }
    return null;
  }

  // ------------------------------------------------------------ on-device check

  Future<void> startCheck() async {
    final s = sub;
    if (s == null) return;
    reportMessage = null;
    await refreshNetwork();
    final j = await core.call('probe.start', {'profile': s.profile, 'options': {}});
    check = Map<String, dynamic>.from(j as Map);
    checkSubId = s.id;
    notifyListeners();
    _checkPoll?.cancel();
    _checkPoll = Timer.periodic(const Duration(milliseconds: 700), (_) async {
      try {
        final v = Map<String, dynamic>.from(await core.call('probe.status', {'id': check!['id']}) as Map);
        check = v;
        notifyListeners();
        if (v['done'] == true) {
          _checkPoll?.cancel();
          if (settings.shareResults) await sendReport();
        }
      } catch (_) {
        _checkPoll?.cancel();
      }
    });
  }

  Future<void> stopCheck() async {
    _checkPoll?.cancel();
    if (check != null) await core.call('probe.stop', {'id': check!['id']});
    check = {...?check, 'done': true};
    notifyListeners();
  }

  Future<void> sendReport() async {
    final s = subs.where((x) => x.id == checkSubId).firstOrNull;
    if (s == null || check == null || network == null) return;
    try {
      await core.call('report.send', {'url': s.url, 'network': network, 'results': check!['results']});
      reportMessage = 'Results shared with the server';
    } catch (e) {
      reportMessage = 'Not shared: $e';
    }
    notifyListeners();
  }

  // ------------------------------------------------------------ one config: speed test, marks, pool, tweaks

  /// Measures one config now, without the whole check.
  Future<void> speedTest(String id) async {
    final s = sub;
    if (s == null) return;
    speed[id] = {'state': 'testing'};
    notifyListeners();
    try {
      final j = Map<String, dynamic>.from(
        await core.call('probe.start', {
          'profile': s.profile,
          'options': {
            'ids': [id],
            'probes': 3,
            'seconds': 5,
          },
        }) as Map,
      );
      for (var i = 0; i < 120; i++) {
        await Future<void>.delayed(const Duration(milliseconds: 500));
        final v = Map<String, dynamic>.from(await core.call('probe.status', {'id': j['id']}) as Map);
        final r = ((v['results'] as List?) ?? []).cast<Map>().where((r) => r['id'] == id).firstOrNull;
        if (r != null) speed[id] = Map<String, dynamic>.from(r);
        notifyListeners();
        if (v['done'] == true) break;
      }
    } catch (e) {
      speed[id] = {'state': 'blocked', 'detail': '$e'};
      notifyListeners();
    }
  }

  Future<void> markConfig(String id, void Function(ConfigMeta m) f) async {
    final s = sub;
    if (s == null) return;
    f(s.metaOf(id));
    s.meta.removeWhere((_, m) => m.empty);
    notifyListeners();
    await _save();
  }

  Future<void> setPool(List<String> ids) async {
    final s = sub;
    if (s == null) return;
    s.pool = ids;
    notifyListeners();
    await _save();
    if (connected && settings.selected == null) await connect();
  }

  Future<void> setConfigTweaks(String id, Map<String, dynamic> t) async {
    await markConfig(id, (m) => m.tweaks = t);
    if (connected) await connect();
  }

  // ------------------------------------------------------------ diagnostics

  Future<String> logTail() async => ((await core.call('logs.tail', {'bytes': 64 * 1024})) as Map)['log'] as String? ?? '';

  /// A bug report: the core's diagnostics with the app's settings (no secrets; the core redacts).
  Future<String> diagnostics() async {
    final st = settings.toJson()..remove('trustedWifi');
    final app = {
      'platform': Platform.operatingSystem,
      'os': Platform.operatingSystemVersion,
      'settings': st,
      'subscriptions': subs.map((s) => {'configs': s.allConfigs.length, 'local': s.local, 'fetched': s.fetched.toIso8601String()}).toList(),
    };
    return ((await core.call('diag.bundle', {'app': app})) as Map)['text'] as String? ?? '';
  }

  // ------------------------------------------------------------ servers (manage)

  Future<Map<String, dynamic>> serverConn(ServerEntry s) async {
    return {
      'id': s.id,
      'name': s.name,
      'host': s.host,
      'port': s.port,
      'user': s.user,
      'hostKey': s.hostKey,
      'password': await Secrets.read('srv-${s.id}-password') ?? '',
      'key': await Secrets.read('srv-${s.id}-key') ?? '',
      'keyPass': await Secrets.read('srv-${s.id}-keypass') ?? '',
    };
  }

  /// Connects to a new server, pins its host key and keeps it.
  Future<Map<String, dynamic>> addServer(ServerEntry s, {String password = '', String key = '', String keyPass = ''}) async {
    final conn = {'name': s.name, 'host': s.host, 'port': s.port, 'user': s.user, 'password': password, 'key': key, 'keyPass': keyPass};
    final r = Map<String, dynamic>.from(await core.call('manage.connect', conn) as Map);
    s.hostKey = r['hostKey'] as String? ?? '';
    if (password.isNotEmpty) await Secrets.write('srv-${s.id}-password', password);
    if (key.isNotEmpty) await Secrets.write('srv-${s.id}-key', key);
    if (keyPass.isNotEmpty) await Secrets.write('srv-${s.id}-keypass', keyPass);
    servers.removeWhere((x) => x.id == s.id);
    servers.add(s);
    activeServerId = s.id;
    notifyListeners();
    await _save();
    return r;
  }

  Future<void> removeServer(ServerEntry s) async {
    try {
      await core.call('manage.disconnect', await serverConn(s));
    } catch (_) {}
    for (final k in ['password', 'key', 'keypass']) {
      await Secrets.delete('srv-${s.id}-$k');
    }
    servers.remove(s);
    if (activeServerId == s.id) activeServerId = servers.firstOrNull?.id;
    notifyListeners();
    await _save();
  }

  Future<void> selectServer(ServerEntry s) async {
    activeServerId = s.id;
    notifyListeners();
    await _save();
  }

  Future<dynamic> manage(String method, ServerEntry s, [Map<String, dynamic> extra = const {}]) async {
    final conn = await serverConn(s);
    if (extra.isEmpty) return core.call(method, conn);
    return core.call(method, {'server': conn, ...extra});
  }
}
