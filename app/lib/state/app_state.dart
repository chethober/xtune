import 'dart:async';
import 'dart:io';
import 'dart:math';

import 'package:flutter/foundation.dart';

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

  Sub? get sub => subs.where((s) => s.id == activeSubId).firstOrNull ?? subs.firstOrNull;
  ServerEntry? get server => servers.where((s) => s.id == activeServerId).firstOrNull ?? servers.firstOrNull;
  bool get connected => status['running'] == true;

  static String newId() => '${DateTime.now().millisecondsSinceEpoch}${Random().nextInt(1 << 20)}';

  Future<void> init() async {
    _prefs = await Prefs.open();
    settings = Settings.fromJson((_prefs.getJson('settings') as Map?)?.cast<String, dynamic>());
    subs = ((_prefs.getJson('subs') as List?) ?? []).map((j) => Sub.fromJson(Map<String, dynamic>.from(j as Map))).toList();
    activeSubId = _prefs.getJson('activeSub') as String?;
    servers = ((_prefs.getJson('servers') as List?) ?? [])
        .map((j) => ServerEntry.fromJson(Map<String, dynamic>.from(j as Map)))
        .toList();
    activeServerId = _prefs.getJson('activeServer') as String?;
    notifyListeners();
    unawaited(_afterStart());
  }

  Future<void> _afterStart() async {
    await refreshStatus();
    await refreshNetwork();
    final now = DateTime.now();
    for (final s in List.of(subs)) {
      if (now.difference(s.fetched) > const Duration(hours: 3)) {
        try {
          await refreshSub(s);
        } catch (_) {}
      }
    }
    _poll = Timer.periodic(const Duration(seconds: 1), (_) => refreshStatus());
  }

  @override
  void dispose() {
    _poll?.cancel();
    _checkPoll?.cancel();
    super.dispose();
  }

  Future<void> _save() async {
    await _prefs.setJson('settings', settings.toJson());
    await _prefs.setJson('subs', subs.map((s) => s.toJson()).toList());
    await _prefs.setJson('activeSub', activeSubId);
    await _prefs.setJson('servers', servers.map((s) => s.toJson()).toList());
    await _prefs.setJson('activeServer', activeServerId);
  }

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

  Future<Sub> addSub(String link) async {
    final norm = await core.call('profile.normalize', {'url': link}) as Map;
    final url = norm['url'] as String;
    final existing = subs.where((s) => s.url == url).firstOrNull;
    final profile = Map<String, dynamic>.from(await core.call('profile.fetch', {'url': url, 'asn': _asn}) as Map);
    final s = existing ?? Sub(id: newId(), url: url, profile: profile, fetched: DateTime.now());
    s.profile = profile;
    s.fetched = DateTime.now();
    if (existing == null) subs.add(s);
    activeSubId = s.id;
    notifyListeners();
    await _save();
    return s;
  }

  Future<void> refreshSub(Sub s) async {
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

  Map<String, dynamic> _options() => {
        'mode': settings.mode == 'vpn' ? 'tun' : 'proxy',
        'selected': settings.selected ?? '',
        'shareLan': settings.shareLan,
        'blockAds': settings.blockAds,
        'domestic': settings.domestic,
        'includeDnstt': settings.includeDnstt,
        'dns': settings.dns,
        'socksPort': settings.socksPort,
        'httpPort': settings.httpPort,
      };

  Future<void> refreshStatus() async {
    try {
      final s = await core.call('vpn.status');
      status = Map<String, dynamic>.from(s as Map);
      notifyListeners();
    } catch (_) {}
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
    final conn = {
      'name': s.name,
      'host': s.host,
      'port': s.port,
      'user': s.user,
      'password': password,
      'key': key,
      'keyPass': keyPass,
    };
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
