/// A subscription the user added: its link and the last profile fetched from it.
class Sub {
  Sub({required this.id, required this.url, required this.profile, required this.fetched});
  final String id;
  final String url;
  Map<String, dynamic> profile;
  DateTime fetched;

  String get name => (profile['name'] as String?)?.isNotEmpty == true ? profile['name'] as String : 'Subscription';
  String get user => profile['user'] as String? ?? '';
  String? get network => profile['network'] as String?;
  List<Config> get configs =>
      ((profile['configs'] as List?) ?? []).map((c) => Config(Map<String, dynamic>.from(c as Map))).toList();
  Usage get usage => Usage(Map<String, dynamic>.from((profile['usage'] as Map?) ?? {}));

  Map<String, dynamic> toJson() => {'id': id, 'url': url, 'profile': profile, 'fetched': fetched.millisecondsSinceEpoch};
  static Sub fromJson(Map<String, dynamic> j) => Sub(
      id: j['id'] as String,
      url: j['url'] as String,
      profile: Map<String, dynamic>.from(j['profile'] as Map),
      fetched: DateTime.fromMillisecondsSinceEpoch(j['fetched'] as int? ?? 0));
}

class Config {
  Config(this.j);
  final Map<String, dynamic> j;
  String get id => j['id'] as String? ?? '';
  String get key => j['key'] as String? ?? '';
  String get name => j['name'] as String? ?? id;
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
      hostKey: j['hostKey'] as String? ?? '');
}

class Settings {
  String mode = 'vpn'; // vpn (whole device) | proxy
  String? selected; // null: Auto
  bool shareLan = false;
  bool domestic = true;
  bool blockAds = true;
  bool includeDnstt = false;
  bool shareResults = true;
  String dns = 'https://1.1.1.1/dns-query';
  String lang = 'en';
  String sort = 'recommended'; // configs page: recommended | latency | name | type
  String perAppMode = 'off'; // Android: off | bypass (the listed apps skip the VPN) | only (only they use it)
  List<String> apps = [];
  int socksPort = 10808;
  int httpPort = 10809;

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
        'dns': dns,
        'lang': lang,
        'sort': sort,
        'perAppMode': perAppMode,
        'apps': apps,
        'socksPort': socksPort,
        'httpPort': httpPort,
      };

  static Settings fromJson(Map<String, dynamic>? j) {
    final s = Settings();
    if (j == null) return s;
    s.mode = j['mode'] as String? ?? s.mode;
    s.selected = j['selected'] as String?;
    s.shareLan = j['shareLan'] as bool? ?? s.shareLan;
    s.domestic = j['domestic'] as bool? ?? s.domestic;
    s.blockAds = j['blockAds'] as bool? ?? s.blockAds;
    s.includeDnstt = j['includeDnstt'] as bool? ?? s.includeDnstt;
    s.shareResults = j['shareResults'] as bool? ?? s.shareResults;
    s.dns = j['dns'] as String? ?? s.dns;
    s.lang = j['lang'] as String? ?? s.lang;
    s.sort = j['sort'] as String? ?? s.sort;
    s.perAppMode = j['perAppMode'] as String? ?? s.perAppMode;
    s.apps = ((j['apps'] as List?) ?? []).cast<String>();
    final old = ((j['excludedApps'] as List?) ?? []).cast<String>(); // 1.0.0 had a bypass list only
    if (s.apps.isEmpty && old.isNotEmpty) {
      s.apps = old;
      s.perAppMode = 'bypass';
    }
    s.socksPort = j['socksPort'] as int? ?? s.socksPort;
    s.httpPort = j['httpPort'] as int? ?? s.httpPort;
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
