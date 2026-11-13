/// A subscription the user added (or links they pasted: no URL), the last profile fetched from it,
/// and what the user did to its configs.
class Sub {
  Sub({required this.id, required this.url, required this.profile, required this.fetched, Map<String, ConfigMeta>? meta, List<String>? pool})
    : meta = meta ?? {},
      pool = pool ?? [];
  final String id;
  final String url; // empty: pasted links, nothing to refresh
  Map<String, dynamic> profile;
  DateTime fetched;
  final Map<String, ConfigMeta> meta; // by config ID
  List<String> pool; // Auto's configs in priority order; empty: all

  bool get local => url.isEmpty;
  String get name => (profile['name'] as String?)?.isNotEmpty == true ? profile['name'] as String : 'Subscription';
  String get user => profile['user'] as String? ?? '';
  String? get network => profile['network'] as String?;
  List<Config> get allConfigs =>
      ((profile['configs'] as List?) ?? []).map((c) => Config(Map<String, dynamic>.from(c as Map), meta[c['id']])).toList();
  List<Config> get configs => allConfigs.where((c) => !c.hidden).toList();
  Usage get usage => Usage(Map<String, dynamic>.from((profile['usage'] as Map?) ?? {}));
  ConfigMeta metaOf(String id) => meta.putIfAbsent(id, ConfigMeta.new);

  Map<String, dynamic> toJson() => {
    'id': id,
    'url': url,
    'profile': profile,
    'fetched': fetched.millisecondsSinceEpoch,
    'meta': meta.map((k, v) => MapEntry(k, v.toJson())),
    'pool': pool,
  };
  static Sub fromJson(Map<String, dynamic> j) => Sub(
    id: j['id'] as String,
    url: j['url'] as String? ?? '',
    profile: Map<String, dynamic>.from(j['profile'] as Map),
    fetched: DateTime.fromMillisecondsSinceEpoch(j['fetched'] as int? ?? 0),
    meta: ((j['meta'] as Map?) ?? {}).map((k, v) => MapEntry(k as String, ConfigMeta.fromJson(Map<String, dynamic>.from(v as Map)))),
    pool: ((j['pool'] as List?) ?? []).cast<String>(),
  );
}

/// The user's marks on a config: starred, hidden, renamed, and its own tweaks.
class ConfigMeta {
  bool star = false;
  bool hidden = false;
  String rename = '';
  Map<String, dynamic> tweaks = {};

  bool get empty => !star && !hidden && rename.isEmpty && tweaks.isEmpty;
  Map<String, dynamic> toJson() => {'star': star, 'hidden': hidden, 'rename': rename, 'tweaks': tweaks};
  static ConfigMeta fromJson(Map<String, dynamic> j) => ConfigMeta()
    ..star = j['star'] == true
    ..hidden = j['hidden'] == true
    ..rename = j['rename'] as String? ?? ''
    ..tweaks = Map<String, dynamic>.from((j['tweaks'] as Map?) ?? {});
}

class Config {
  Config(this.j, [this.meta]);
  final Map<String, dynamic> j;
  final ConfigMeta? meta;
  String get id => j['id'] as String? ?? '';
  String get key => j['key'] as String? ?? '';
  String get serverName => j['name'] as String? ?? id;
  String get name => meta?.rename.isNotEmpty == true ? meta!.rename : serverName;
  bool get star => meta?.star ?? false;
  bool get hidden => meta?.hidden ?? false;
  String get kind => j['kind'] as String? ?? '';
  String get label => (j['label'] as String?)?.isNotEmpty == true ? j['label'] as String : kind;
  String get engine => j['engine'] as String? ?? '';
  String get link => j['link'] as String? ?? '';
  bool get handoff => engine == 'handoff';
  bool get dnstt => (j['ssh'] as Map?)?['dnstt'] != null;
  Map<String, dynamic>? get file => j['file'] == null ? null : Map<String, dynamic>.from(j['file'] as Map);

  /// The family the configs screen groups by.
  String get group {
    if (handoff) return kind == 'mtproto' ? 'Tunnels' : 'VPN';
    if (engine == 'awg' || kind == 'wireguard') return 'VPN';
    if (engine == 'ssh') return 'Tunnels';
    return 'Xray';
  }

  String get engineLabel => switch (engine) {
    'xray' => 'Xray',
    'awg' => 'AmneziaWG',
    'ssh' => dnstt ? 'dnstt + SSH' : 'SSH',
    _ => kind == 'mtproto' ? 'Telegram' : (kind == 'ikev2' ? 'System' : 'OpenVPN'),
  };
}

class Usage {
  Usage(this.j);
  final Map<String, dynamic> j;
  int get up => (j['up'] as num?)?.toInt() ?? 0;
  int get down => (j['down'] as num?)?.toInt() ?? 0;
  int get used => up + down;
  int get quota => (j['quota'] as num?)?.toInt() ?? 0;
  int get expires => (j['expires'] as num?)?.toInt() ?? 0;
  int get resets => (j['resets'] as num?)?.toInt() ?? 0;
  int get devices => (j['devices'] as num?)?.toInt() ?? 0;
  bool get disabled => j['disabled'] == true;
  String get page => j['page'] as String? ?? '';
  double get fraction => quota <= 0 ? 0 : (used / quota).clamp(0, 1).toDouble();
  int? get daysLeft => expires <= 0 ? null : DateTime.fromMillisecondsSinceEpoch(expires * 1000).difference(DateTime.now()).inDays;
}

/// One of the operator's servers. Its password and key live in Secrets, not here.
class ServerEntry {
  ServerEntry({required this.id, required this.name, required this.host, this.port = 22, this.user = 'root', this.hostKey = ''});
  final String id;
  String name;
  String host;
  int port;
  String user;
  String hostKey;

  Map<String, dynamic> toJson() => {'id': id, 'name': name, 'host': host, 'port': port, 'user': user, 'hostKey': hostKey};
  static ServerEntry fromJson(Map<String, dynamic> j) => ServerEntry(
    id: j['id'] as String,
    name: j['name'] as String? ?? '',
    host: j['host'] as String? ?? '',
    port: j['port'] as int? ?? 22,
    user: j['user'] as String? ?? 'root',
    hostKey: j['hostKey'] as String? ?? '',
  );
}

/// A routing rule: matching traffic goes direct, through the proxy, or nowhere.
class RouteRule {
  RouteRule({required this.match, required this.value, required this.action});
  String match; // domain | full | keyword | geosite | ip | geoip | port
  String value;
  String action; // direct | proxy | block
  Map<String, dynamic> toJson() => {'match': match, 'value': value, 'action': action};
  static RouteRule fromJson(Map<String, dynamic> j) =>
      RouteRule(match: j['match'] as String? ?? 'domain', value: j['value'] as String? ?? '', action: j['action'] as String? ?? 'direct');
}

/// The app's settings. Stored as JSON; a field missing in an older file reads as its default.
class Settings {
  String mode = 'vpn'; // vpn (whole device) | proxy
  String? selected; // null: Auto
  bool shareLan = false;
  bool domestic = true;
  bool blockAds = true;
  bool includeDnstt = false;
  bool shareResults = true;
  String lang = 'en';
  String sort = 'recommended'; // configs page: recommended | latency | name | type
  String perAppMode = 'off'; // Android: off | bypass (the listed apps skip the VPN) | only (only they use it)
  List<String> apps = [];
  int socksPort = 10808;
  int httpPort = 10809;

  // appearance
  String theme = 'system'; // system | dark | black | light
  String accent = 'green'; // green | blue | purple | orange

  // connection
  bool autoConnectLaunch = false;
  bool autoConnectBoot = false; // Android
  bool autoConnectWifi = false; // Android: on Wi-Fi networks not in trusted
  List<String> trustedWifi = [];
  bool smartReconnect = true;
  bool killSwitch = false; // Linux desktops; Android uses always-on VPN
  Map<String, dynamic> tweaks = {}; // for every config: fragment, fragLength, fragDelay, fingerprint, mtu, mux

  // routing and DNS
  String dns = 'https://1.1.1.1/dns-query';
  String domesticDns = ''; // e.g. udp://217.218.155.155; empty: the same resolver
  bool fakeDns = false;
  bool ipv6 = false;
  bool blockQuic = false;
  bool proxyLan = false;
  bool bypassPush = false;
  List<String> bypassDomains = [];
  List<RouteRule> rules = [];

  // subscriptions
  int autoUpdateHours = 6; // 0: never

  /// The apps Android leaves out of the VPN, for the bypass mode.
  List<String> get excludedApps => perAppMode == 'bypass' ? apps : const [];

  Map<String, dynamic> toJson() => {
    'mode': mode,
    'selected': selected,
    'shareLan': shareLan,
    'domestic': domestic,
    'blockAds': blockAds,
    'includeDnstt': includeDnstt,
    'shareResults': shareResults,
    'lang': lang,
    'sort': sort,
    'perAppMode': perAppMode,
    'apps': apps,
    'socksPort': socksPort,
    'httpPort': httpPort,
    'theme': theme,
    'accent': accent,
    'autoConnectLaunch': autoConnectLaunch,
    'autoConnectBoot': autoConnectBoot,
    'autoConnectWifi': autoConnectWifi,
    'trustedWifi': trustedWifi,
    'smartReconnect': smartReconnect,
    'killSwitch': killSwitch,
    'tweaks': tweaks,
    'dns': dns,
    'domesticDns': domesticDns,
    'fakeDns': fakeDns,
    'ipv6': ipv6,
    'blockQuic': blockQuic,
    'proxyLan': proxyLan,
    'bypassPush': bypassPush,
    'bypassDomains': bypassDomains,
    'rules': rules.map((r) => r.toJson()).toList(),
    'autoUpdateHours': autoUpdateHours,
  };

  static Settings fromJson(Map<String, dynamic>? j) {
    final s = Settings();
    if (j == null) return s;
    T get<T>(String k, T d) => j[k] is T ? j[k] as T : d;
    List<String> strings(String k) => ((j[k] as List?) ?? []).cast<String>();
    s.mode = get('mode', s.mode);
    s.selected = j['selected'] as String?;
    s.shareLan = get('shareLan', s.shareLan);
    s.domestic = get('domestic', s.domestic);
    s.blockAds = get('blockAds', s.blockAds);
    s.includeDnstt = get('includeDnstt', s.includeDnstt);
    s.shareResults = get('shareResults', s.shareResults);
    s.lang = get('lang', s.lang);
    s.sort = get('sort', s.sort);
    s.perAppMode = get('perAppMode', s.perAppMode);
    s.apps = strings('apps');
    final old = strings('excludedApps'); // 1.0.0 had a bypass list only
    if (s.apps.isEmpty && old.isNotEmpty) {
      s.apps = old;
      s.perAppMode = 'bypass';
    }
    s.socksPort = get('socksPort', s.socksPort);
    s.httpPort = get('httpPort', s.httpPort);
    s.theme = get('theme', s.theme);
    s.accent = get('accent', s.accent);
    s.autoConnectLaunch = get('autoConnectLaunch', s.autoConnectLaunch);
    s.autoConnectBoot = get('autoConnectBoot', s.autoConnectBoot);
    s.autoConnectWifi = get('autoConnectWifi', s.autoConnectWifi);
    s.trustedWifi = strings('trustedWifi');
    s.smartReconnect = get('smartReconnect', s.smartReconnect);
    s.killSwitch = get('killSwitch', s.killSwitch);
    s.tweaks = Map<String, dynamic>.from(get<Map>('tweaks', {}));
    s.dns = get('dns', s.dns);
    s.domesticDns = get('domesticDns', s.domesticDns);
    s.fakeDns = get('fakeDns', s.fakeDns);
    s.ipv6 = get('ipv6', s.ipv6);
    s.blockQuic = get('blockQuic', s.blockQuic);
    s.proxyLan = get('proxyLan', s.proxyLan);
    s.bypassPush = get('bypassPush', s.bypassPush);
    s.bypassDomains = strings('bypassDomains');
    s.rules = ((j['rules'] as List?) ?? []).map((r) => RouteRule.fromJson(Map<String, dynamic>.from(r as Map))).toList();
    s.autoUpdateHours = get('autoUpdateHours', s.autoUpdateHours);
    return s;
  }
}

String fmtBytes(num b) {
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  var v = b.toDouble();
  var i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return i == 0 ? '${v.toInt()} B' : '${v.toStringAsFixed(v >= 100 ? 0 : 1)} ${units[i]}';
}

String fmtDuration(Duration d) {
  String two(int n) => n.toString().padLeft(2, '0');
  return '${two(d.inHours)}:${two(d.inMinutes % 60)}:${two(d.inSeconds % 60)}';
}
