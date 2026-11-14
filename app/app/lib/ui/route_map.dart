import 'dart:async';
import 'dart:convert';
import 'dart:math' as math;
import 'dart:typed_data';
import 'dart:ui' as ui;

import 'package:material_ui/material_ui.dart';

import '../state/app_state.dart';
import '../state/models.dart';
import 'glass.dart';
import 'i18n.dart';
import 'world.dart';

/// The home screen's map: this device, the relays and servers the subscription's configs go
/// through, and the delay of each hop. The route in use is drawn in the accent with traffic
/// moving along it; the others are faint.
///
/// Where a config goes comes from its name (servers put the flags of both ends in it, relay
/// first: "🇮🇷→🇳🇱"). The first hop's delay is a TCP handshake to the address the config connects
/// to; the rest is the config's whole delay less that, so it is an estimate (≈).
class RouteMap extends StatefulWidget {
  const RouteMap({super.key, required this.state});
  final AppState state;

  @override
  State<RouteMap> createState() => _RouteMapState();
}

/// One way out: the countries it passes (entry first), the address it connects to, and the
/// configs that take it.
class MapRoute {
  MapRoute(this.countries, this.host);
  final List<String> countries;
  final String host;
  final List<Config> configs = [];
  int? total; // the best delay of its configs; -1: none works
  Config? best;

  String get key => '${countries.join('>')}@$host';
  bool get relay => countries.length > 1;
}

const _ri0 = 0x1F1E6, _ri1 = 0x1F1FF;

/// The countries a config's name names, in order: flag emoji, or codes around an arrow ("IR→NL").
List<String> countriesIn(String name) {
  final out = <String>[];
  final r = name.runes.toList();
  for (var i = 0; i + 1 < r.length; i++) {
    if (r[i] >= _ri0 && r[i] <= _ri1 && r[i + 1] >= _ri0 && r[i + 1] <= _ri1) {
      out.add(String.fromCharCodes([r[i] - _ri0 + 65, r[i + 1] - _ri0 + 65]));
      i++;
    }
  }
  if (out.isEmpty) {
    for (final m in RegExp(r'(?<![A-Za-z])([A-Z]{2})\s*(?:→|->)\s*([A-Z]{2})(?![A-Za-z])').allMatches(name)) {
      out.addAll([m[1]!, m[2]!]);
    }
  }
  final known = <String>[];
  for (final c in out) {
    if (countryPlaces.containsKey(c) && (known.isEmpty || known.last != c)) known.add(c);
  }
  return known;
}

/// The routes of a subscription's configs, the one in use first, then the fastest.
List<MapRoute> routesOf(AppState s) {
  final sub = s.sub;
  if (sub == null) return [];
  final byKey = <String, MapRoute>{};
  for (final c in sub.configs) {
    var cs = countriesIn(c.name);
    if (cs.isEmpty) cs = countriesIn(c.serverName);
    if (cs.isEmpty) continue;
    final host = c.j['server'] as String? ?? '';
    final r = byKey.putIfAbsent('${cs.join('>')}@$host', () => MapRoute(cs, host));
    r.configs.add(c);
    final d = s.delayFor(c.id);
    if (d != null && d > 0 && (r.total == null || r.total! <= 0 || d < r.total!)) {
      r.total = d;
      r.best = c;
    } else if (d == -1 && r.total == null) {
      r.total = -1;
    }
  }
  final active = s.connected ? s.activeConfig?.id : null;
  int rank(MapRoute r) => r.configs.any((c) => c.id == active) ? -1 : (r.total == null || r.total! <= 0 ? 1 << 30 : r.total!);
  return byKey.values.toList()..sort((a, b) => rank(a).compareTo(rank(b)));
}

class _RouteMapState extends State<RouteMap> with SingleTickerProviderStateMixin {
  late final AnimationController _flow = AnimationController(vsync: this, duration: const Duration(milliseconds: 1800));
  Timer? _timer;
  Map<String, int> _rtt = {};
  Set<String> _measured = {};
  bool _measuring = false;

  @override
  void initState() {
    super.initState();
    _timer = Timer.periodic(const Duration(seconds: 30), (_) => _measure(force: true));
    WidgetsBinding.instance.addPostFrameCallback((_) => _measure());
  }

  @override
  void dispose() {
    _timer?.cancel();
    _flow.dispose();
    super.dispose();
  }

  /// Times the first hop of every route (again every 30 seconds, or when the routes change).
  Future<void> _measure({bool force = false}) async {
    if (!mounted || _measuring || !TickerMode.valuesOf(context).enabled) return;
    final hosts = routesOf(widget.state).map((r) => r.host).where((h) => h.isNotEmpty).toSet();
    if (hosts.isEmpty || (!force && hosts.difference(_measured).isEmpty)) return;
    _measuring = true;
    try {
      final r = await widget.state.hostRtts(hosts.take(24).toList());
      if (mounted) setState(() => _rtt = r);
      _measured = hosts;
    } catch (_) {
    } finally {
      _measuring = false;
    }
  }

  @override
  Widget build(BuildContext context) {
    final s = widget.state;
    final routes = routesOf(s);
    if (routes.isEmpty) return const SizedBox.shrink();
    WidgetsBinding.instance.addPostFrameCallback((_) => _measure());
    final activeId = s.connected ? s.activeConfig?.id : null;
    final active = activeId == null ? null : routes.where((r) => r.configs.any((c) => c.id == activeId)).firstOrNull;
    if (active != null && !_flow.isAnimating) {
      _flow.repeat();
    } else if (active == null && _flow.isAnimating) {
      _flow.stop();
    }
    final home = (s.sub?.profile['country'] as String? ?? '').toUpperCase();
    final shown = routes.take(4).toList();
    return Glass(
      padding: EdgeInsets.zero,
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Padding(
            padding: const EdgeInsets.fromLTRB(Space.l, Space.l, Space.l, Space.s),
            child: Text(T.s('routeMap').toUpperCase(), style: Type.overline),
          ),
          ClipRRect(
            child: SizedBox(
              height: 190,
              child: RepaintBoundary(
                child: CustomPaint(
                  painter: _MapPainter(
                    routes: routes.take(12).toList(),
                    active: active,
                    home: countryPlaces.containsKey(home) ? home : null,
                    rtt: _rtt,
                    flow: _flow,
                    palette: X.p,
                    labelStyle: Type.badge,
                    you: T.s('you'),
                  ),
                  size: Size.infinite,
                ),
              ),
            ),
          ),
          const SizedBox(height: Space.xs),
          for (final r in shown)
            _RouteRow(
              route: r,
              active: identical(r, active),
              rtt: _rtt[r.host],
              onTap: r.best == null && r.configs.isEmpty ? null : () => s.select((r.best ?? r.configs.first).id),
            ),
          const SizedBox(height: Space.s),
        ],
      ),
    );
  }
}

/// The delays of a route's hops: the first measured, the rest the remainder of the whole.
(int?, int?) hopDelays(MapRoute r, int? rtt) {
  final first = rtt != null && rtt > 0 ? rtt : null;
  final total = r.total != null && r.total! > 0 ? r.total : null;
  final rest = first != null && total != null && total > first ? total - first : null;
  return (first, rest);
}

class _RouteRow extends StatelessWidget {
  const _RouteRow({required this.route, required this.active, required this.rtt, required this.onTap});
  final MapRoute route;
  final bool active;
  final int? rtt;
  final VoidCallback? onTap;

  @override
  Widget build(BuildContext context) {
    final (first, rest) = hopDelays(route, rtt);
    final dead = route.total == -1;
    final color = active ? X.accent : (dead ? X.bad : X.muted);
    final hops = route.countries.join(' → ');
    final parts = [if (first != null) '$first', if (rest != null) '≈$rest'];
    return InkWell(
      onTap: onTap,
      child: Padding(
        padding: const EdgeInsets.symmetric(horizontal: Space.l, vertical: 7),
        child: Row(
          children: [
            Dot(color, size: 8),
            const SizedBox(width: Space.s),
            Expanded(
              child: Text.rich(
                TextSpan(
                  children: [
                    TextSpan(
                      text: '${T.s('you')} → $hops',
                      style: Type.body.copyWith(fontSize: 14, color: active ? X.accent : X.text),
                    ),
                    if (route.relay) TextSpan(text: '  ${T.s('relayHop')}', style: Type.caption),
                  ],
                ),
                maxLines: 1,
                overflow: TextOverflow.ellipsis,
              ),
            ),
            if (dead)
              Text('—', style: Type.number.copyWith(color: X.bad))
            else if (parts.isNotEmpty || route.total != null)
              Text(
                [if (parts.isNotEmpty) parts.join(' + '), if (route.total != null && route.total! > 0) '${route.total} ms'].join(' = '),
                style: Type.number.copyWith(fontSize: 13, color: active ? X.accent : X.muted),
              ),
          ],
        ),
      ),
    );
  }
}

/// The land, decoded once.
final Uint8List _land = base64Decode(landBits);
bool _isLand(int row, int col) {
  if (row < 0 || row >= landTop - landBottom) return false;
  col %= 360;
  return (_land[row * 45 + (col >> 3)] >> (col & 7)) & 1 == 1;
}

class _MapPainter extends CustomPainter {
  _MapPainter({
    required this.routes,
    required this.active,
    required this.home,
    required this.rtt,
    required this.flow,
    required this.palette,
    required this.labelStyle,
    required this.you,
  }) : super(repaint: flow);
  final List<MapRoute> routes;
  final MapRoute? active;
  final String? home;
  final Map<String, int> rtt;
  final Animation<double> flow;
  final Palette palette;
  final TextStyle labelStyle;
  final String you;

  static (double, double) place(String cc) => countryPlaces[cc]!;

  @override
  void paint(Canvas canvas, Size size) {
    // where everything is: (lon, lat); a node on top of another moves aside a little
    final nodes = <String, Offset>{};
    Offset geo(String cc) => Offset(place(cc).$2, place(cc).$1);
    // the device: the subscription's home country, else beside the first entry
    final firstEntry = (active ?? routes.first).countries.first;
    final youAt = home != null ? geo(home!) : geo(firstEntry) + const Offset(-14, -4);
    for (final r in routes) {
      for (var i = 0; i < r.countries.length; i++) {
        final cc = r.countries[i];
        final id = i == 0 && r.relay ? 'relay:$cc' : 'node:$cc';
        nodes.putIfAbsent(id, () => geo(cc));
      }
    }
    String idOf(MapRoute r, int i) => i == 0 && r.relay ? 'relay:${r.countries[0]}' : 'node:${r.countries[i]}';

    // the view: every node with a margin, at least 50 by 25 degrees, the box's own shape
    final all = [youAt, ...nodes.values];
    var minX = all.map((o) => o.dx).reduce(math.min) - 14, maxX = all.map((o) => o.dx).reduce(math.max) + 14;
    var minY = all.map((o) => o.dy).reduce(math.min) - 7, maxY = all.map((o) => o.dy).reduce(math.max) + 9;
    final aspect = size.width / size.height;
    var w = math.max(maxX - minX, 50.0), h = math.max(maxY - minY, 25.0);
    if (w / h > aspect) {
      h = w / aspect;
    } else {
      w = h * aspect;
    }
    final cx = (minX + maxX) / 2, cy = (minY + maxY) / 2;
    minX = cx - w / 2;
    maxY = cy + h / 2;
    final k = size.width / w; // pixels per degree
    Offset px(Offset g) => Offset((g.dx - minX) * k, (maxY - g.dy) * k);

    // on screen, a node too close to another (a relay in your own country) moves aside
    final at = <String, Offset>{};
    final placed = <Offset>[];
    Offset apart(Offset p) {
      var q = p;
      for (var i = 0; i < 16 && placed.any((t) => (t - q).distance < 30); i++) {
        final a = 0.5 + i * 0.85;
        q = p + Offset(math.cos(a), math.sin(a)) * (32.0 + 10 * (i ~/ 7));
      }
      q = Offset(q.dx.clamp(10, size.width - 10), q.dy.clamp(22, size.height - 10));
      placed.add(q);
      return q;
    }

    at['you'] = apart(px(youAt));
    nodes.forEach((id, g) => at[id] = apart(px(g)));

    // land as dots, about 6 pixels apart
    final step = math.max(1, (6.5 / k).round());
    final dots = <Offset>[];
    final rowTop = math.max(0, (landTop - maxY).floor());
    final rowBottom = math.min(landTop - landBottom, (landTop - (maxY - h)).ceil());
    final colLeft = (minX + 180).floor(), colRight = (minX + w + 180).ceil();
    for (var row = rowTop - rowTop % step; row < rowBottom; row += step) {
      for (var col = colLeft - colLeft % step; col < colRight; col += step) {
        if (_isLand(row, col)) dots.add(px(Offset(col - 180 + step / 2, landTop - row - step / 2)));
      }
    }
    final light = palette.brightness == Brightness.light;
    canvas.drawPoints(
      ui.PointMode.points,
      dots,
      Paint()
        ..color = palette.text.withValues(alpha: light ? 0.14 : 0.16)
        ..strokeWidth = math.min(3.2, math.max(1.8, step * k * 0.42))
        ..strokeCap = StrokeCap.round,
    );

    // the routes: faint ones first, the one in use on top
    final pills = <(Offset, String)>[];
    final ordered = [...routes.where((r) => !identical(r, active)), ?active];
    for (final r in ordered) {
      final on = identical(r, active);
      final dead = r.total == -1;
      final color = on ? palette.accent : (dead ? palette.bad.withValues(alpha: 0.45) : palette.muted.withValues(alpha: 0.4));
      final pts = [at['you']!, for (var i = 0; i < r.countries.length; i++) at[idOf(r, i)]!];
      final paint = Paint()
        ..color = color
        ..style = PaintingStyle.stroke
        ..strokeWidth = on ? 2.4 : 1.2
        ..strokeCap = StrokeCap.round;
      final (first, rest) = hopDelays(r, rtt[r.host]);
      for (var i = 0; i + 1 < pts.length; i++) {
        final path = _arc(pts[i], pts[i + 1]);
        canvas.drawPath(path, paint);
        if (!on) continue;
        // traffic moving along it
        final m = path.computeMetrics().firstOrNull;
        if (m != null && m.length > 0) {
          for (final phase in [0.0, 0.5]) {
            final t = (flow.value + phase) % 1.0;
            final pos = m.getTangentForOffset(m.length * t)?.position;
            if (pos != null) {
              canvas.drawCircle(pos, 5, Paint()..color = palette.accent.withValues(alpha: 0.25));
              canvas.drawCircle(pos, 2.4, Paint()..color = palette.accent);
            }
          }
        }
        // the hop's delay at its middle
        final ms = i == 0 ? first : (i == pts.length - 2 ? rest : null);
        if (ms != null) {
          var mid = m?.getTangentForOffset(m.length / 2)?.position ?? (pts[i] + pts[i + 1]) / 2;
          // a short hop: beside it, not over its ends
          if ((pts[i + 1] - pts[i]).distance < 90) mid += const Offset(0, 22);
          pills.add((mid, i == 0 ? '$ms ms' : '≈$ms ms'));
        }
      }
      if (on && r.countries.length == 1 && rest != null) {
        // a direct route: what is left is the server's own way to the internet
        pills.add((pts.last + const Offset(0, 24), '+≈$rest ms'));
      }
    }

    // the nodes
    void node(Offset p, String label, {required Color color, bool square = false, bool big = false}) {
      final r = big ? 6.0 : 4.5;
      canvas.drawCircle(p, r + 4, Paint()..color = color.withValues(alpha: 0.18));
      final fill = Paint()..color = color;
      if (square) {
        canvas.drawRRect(RRect.fromRectAndRadius(Rect.fromCircle(center: p, radius: r), const Radius.circular(2)), fill);
      } else {
        canvas.drawCircle(p, r, fill);
      }
      canvas.drawCircle(
        p,
        r,
        Paint()
          ..color = palette.bg
          ..style = PaintingStyle.stroke
          ..strokeWidth = 1.5,
      );
      _label(canvas, p + Offset(0, -r - 14), label, color == palette.muted ? palette.text.withValues(alpha: 0.75) : color, size);
    }

    final onNodes = <String>{
      if (active != null)
        for (var i = 0; i < active!.countries.length; i++) i == 0 && active!.relay ? 'relay:${active!.countries[0]}' : 'node:${active!.countries[i]}',
    };
    nodes.forEach((id, _) {
      final relay = id.startsWith('relay:');
      final cc = id.substring(id.indexOf(':') + 1);
      node(at[id]!, cc, color: onNodes.contains(id) ? (relay ? palette.blue : palette.accent) : palette.muted, square: relay);
    });
    // the device, with a pulse while connected
    final yp = at['you']!;
    if (active != null) {
      final t = flow.value;
      canvas.drawCircle(yp, 6 + 14 * t, Paint()..color = palette.blue.withValues(alpha: 0.35 * (1 - t)));
    }
    node(yp, you, color: palette.blue, big: true);
    for (final (p, text) in pills) {
      _pill(canvas, p, text, size);
    }
  }

  /// A gentle arc from a to b, bowed upwards.
  Path _arc(Offset a, Offset b) {
    final mid = (a + b) / 2;
    final d = b - a;
    var n = Offset(-d.dy, d.dx) * 0.22;
    if (n.dy > 0) n = -n;
    return Path()
      ..moveTo(a.dx, a.dy)
      ..quadraticBezierTo(mid.dx + n.dx, mid.dy + n.dy, b.dx, b.dy);
  }

  void _label(Canvas canvas, Offset at, String text, Color color, Size size) {
    final tp = TextPainter(
      text: TextSpan(
        text: text,
        style: labelStyle.copyWith(color: color),
      ),
      textDirection: TextDirection.ltr,
    )..layout();
    final o = Offset((at.dx - tp.width / 2).clamp(2, size.width - tp.width - 2), at.dy.clamp(2, size.height - tp.height - 2));
    tp.paint(canvas, o);
  }

  void _pill(Canvas canvas, Offset at, String text, Size size) {
    final tp = TextPainter(
      text: TextSpan(
        text: text,
        style: labelStyle.copyWith(color: palette.text, fontSize: 10.5),
      ),
      textDirection: TextDirection.ltr,
    )..layout();
    final w = tp.width + 12, h = tp.height + 6;
    final c = Offset(at.dx.clamp(w / 2 + 2, size.width - w / 2 - 2), at.dy.clamp(h / 2 + 2, size.height - h / 2 - 2));
    final rect = RRect.fromRectAndRadius(Rect.fromCenter(center: c, width: w, height: h), Radius.circular(h / 2));
    canvas.drawRRect(rect, Paint()..color = palette.surface.withValues(alpha: 0.92));
    canvas.drawRRect(
      rect,
      Paint()
        ..color = palette.accent.withValues(alpha: 0.6)
        ..style = PaintingStyle.stroke
        ..strokeWidth = 1,
    );
    tp.paint(canvas, c - Offset(tp.width / 2, tp.height / 2));
  }

  @override
  bool shouldRepaint(_MapPainter old) =>
      old.routes != routes || old.active != active || old.rtt != rtt || old.palette != palette || old.home != home || old.you != you;
}
