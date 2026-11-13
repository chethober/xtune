import 'dart:io';

import 'package:flutter/services.dart';
import 'package:material_ui/material_ui.dart';
import 'package:path_provider/path_provider.dart';
import 'package:qr_flutter/qr_flutter.dart';
import 'package:share_plus/share_plus.dart';
import 'package:url_launcher/url_launcher.dart';

import '../main.dart';
import '../state/app_state.dart';
import '../state/models.dart';
import '../ui/glass.dart';
import '../ui/i18n.dart';
import 'add_sub.dart';

class ConfigsScreen extends StatefulWidget {
  const ConfigsScreen({super.key});
  @override
  State<ConfigsScreen> createState() => _ConfigsScreenState();
}

/// What the list knows about one config: how it is doing here and how fast it answered.
class _Row {
  _Row(this.c, this.state, this.ms, this.order);
  final Config c;
  final String state; // ok | warn | bad | ext | unknown
  final int? ms;
  final int order; // the server's order: best first for this network
}

class _ConfigsScreenState extends State<ConfigsScreen> {
  int filter = 0;
  static const groups = ['', 'Xray', 'VPN', 'Tunnels'];
  static const sorts = ['recommended', 'latency', 'name', 'type'];

  List<_Row> _rows(AppState s) {
    final configs = s.sub?.configs ?? [];
    final checked = <String, Map<String, dynamic>>{};
    if (s.check != null && s.checkSubId == s.sub?.id) {
      for (final r in (s.check!['results'] as List? ?? [])) {
        final m = Map<String, dynamic>.from(r as Map);
        if (m['state'] != 'testing') checked[m['id'] as String] = m;
      }
    }
    final rows = <_Row>[];
    for (var i = 0; i < configs.length; i++) {
      final c = configs[i];
      if (filter != 0 && c.group != groups[filter]) continue;
      var state = 'unknown';
      int? ms;
      final delay = s.connected ? s.delayOf(c.id) : null;
      final r = checked[c.id];
      if (c.handoff) {
        state = 'ext';
      } else if (delay != null) {
        state = delay < 0 ? 'bad' : 'ok';
        ms = delay < 0 ? null : delay;
      } else if (r != null) {
        state = switch (r['state']) { 'ok' => 'ok', 'blocked' => 'bad', _ => 'warn' };
        final w = (r['warmMs'] as num?)?.toInt() ?? -1;
        ms = w > 0 ? w : null;
      }
      rows.add(_Row(c, state, ms, i));
    }
    int rank(_Row r) => switch (r.state) { 'ok' => 0, 'warn' => 1, 'unknown' => 2, 'ext' => 3, _ => 4 };
    switch (s.settings.sort) {
      case 'latency':
        rows.sort((a, b) {
          final x = rank(a).compareTo(rank(b));
          if (x != 0) return x;
          return (a.ms ?? 1 << 30).compareTo(b.ms ?? 1 << 30);
        });
      case 'name':
        rows.sort((a, b) => a.c.name.toLowerCase().compareTo(b.c.name.toLowerCase()));
      case 'type':
        rows.sort((a, b) {
          final x = groups.indexOf(a.c.group).compareTo(groups.indexOf(b.c.group));
          return x != 0 ? x : a.c.label.compareTo(b.c.label);
        });
    }
    return rows;
  }

  @override
  Widget build(BuildContext context) {
    final s = AppScope.of(context);
    final sub = s.sub;
    final rows = _rows(s);
    return XPage(
      title: T.s('configs'),
      subtitle: sub == null ? null : [if (sub.network != null) T.s('rankedFor', {'net': sub.network!}), T.s('updated', {'when': _ago(sub.fetched)})].join(' · '),
      onRefresh: () async {
        if (sub == null) return;
        try {
          await s.refreshSub(sub);
        } catch (e) {
          if (context.mounted) ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text('$e')));
        }
      },
      actions: [
        PopupMenuButton<String>(
          tooltip: T.s('sort'),
          initialValue: s.settings.sort,
          onSelected: (v) => s.updateSettings((x) => x.sort = v),
          itemBuilder: (_) => [
            for (final k in sorts)
              CheckedPopupMenuItem(value: k, checked: s.settings.sort == k, child: Text(T.s('sort${k[0].toUpperCase()}${k.substring(1)}'))),
          ],
          child: Padding(
            padding: const EdgeInsetsDirectional.only(start: Space.s),
            child: Container(
              width: 44,
              height: 44,
              decoration: BoxDecoration(color: X.glass, shape: BoxShape.circle, border: Border.all(color: X.edge)),
              child: const Icon(Icons.sort_rounded, size: 20),
            ),
          ),
        ),
        RoundIconButton(icon: Icons.add_rounded, tooltip: T.s('addSub'), filled: true, onPressed: () => showAddSub(context)),
      ],
      children: [
        Pills(labels: [T.s('all'), 'Xray', 'VPN', 'Tunnels'], selected: filter, onSelect: (i) => setState(() => filter = i)),
        const SizedBox(height: Space.m),
        if (sub != null)
          _ConfigRow(
            title: T.s('auto'),
            meta: T.s('bestHere'),
            trailing: '',
            engine: '',
            dot: X.accent,
            selected: s.settings.selected == null,
            onTap: () => s.select(null),
          ),
        for (final r in rows)
          Padding(
            key: ValueKey(r.c.id),
            padding: const EdgeInsets.only(top: Space.s),
            child: _ConfigRow(
              title: r.c.name,
              meta: switch (r.state) {
                'ext' => '${r.c.label} · ${T.s('runsElsewhere')}',
                'bad' => '${r.c.label} · ${T.s('blockedHere')}',
                _ => r.c.label,
              },
              trailing: switch (r.state) { 'ext' => T.s('openInApp'), 'bad' => '—', _ => r.ms == null ? '' : '${r.ms} ms' },
              engine: r.c.engineLabel,
              dot: X.status(r.state),
              selected: s.settings.selected == r.c.id,
              onTap: () => r.c.handoff ? _handoff(context, r.c) : s.select(r.c.id),
              onLongPress: () => _details(context, r.c),
            ),
          ),
      ],
    );
  }
}

String _ago(DateTime t) {
  final d = DateTime.now().difference(t);
  if (d.inMinutes < 1) return T.rtl ? 'همین الان' : 'just now';
  if (d.inHours < 1) return T.rtl ? '${d.inMinutes} دقیقه پیش' : '${d.inMinutes} min ago';
  if (d.inDays < 1) return T.rtl ? '${d.inHours} ساعت پیش' : '${d.inHours} h ago';
  return T.rtl ? '${d.inDays} روز پیش' : '${d.inDays} d ago';
}

class _ConfigRow extends StatelessWidget {
  const _ConfigRow(
      {required this.title,
      required this.meta,
      required this.trailing,
      required this.engine,
      required this.dot,
      required this.selected,
      required this.onTap,
      this.onLongPress});
  final String title, meta, trailing, engine;
  final Color dot;
  final bool selected;
  final VoidCallback onTap;
  final VoidCallback? onLongPress;

  @override
  Widget build(BuildContext context) {
    return Semantics(
      selected: selected,
      button: true,
      child: GestureDetector(
        onLongPress: onLongPress,
        child: AnimatedContainer(
          duration: Motion.normal,
          child: Glass(
            radius: Radii.l,
            padding: const EdgeInsets.symmetric(horizontal: Space.l, vertical: Space.m),
            color: selected ? X.accent.withValues(alpha: 0.14) : null,
            border: selected ? X.accent.withValues(alpha: 0.45) : null,
            onTap: onTap,
            child: Row(children: [
              Dot(dot, size: 10),
              const SizedBox(width: Space.m),
              Expanded(
                child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
                  Text(title, style: Type.bodyStrong, overflow: TextOverflow.ellipsis),
                  const SizedBox(height: 3),
                  Text(meta, style: Type.caption, overflow: TextOverflow.ellipsis),
                ]),
              ),
              const SizedBox(width: Space.s),
              Column(crossAxisAlignment: CrossAxisAlignment.end, children: [
                Text(trailing, style: Type.number.copyWith(color: dot == X.bad ? X.bad : (dot == X.blue ? X.blue : X.text))),
                if (engine.isNotEmpty) ...[const SizedBox(height: 3), Text(engine, style: Type.caption)],
              ]),
            ]),
          ),
        ),
      ),
    );
  }
}

/// Configs another app runs: Telegram opens its proxy link; VPN profiles are saved and shared.
Future<void> _handoff(BuildContext context, Config c) async {
  if (c.kind == 'mtproto' && c.link.isNotEmpty) {
    if (!await launchUrl(Uri.parse(c.link), mode: LaunchMode.externalApplication)) {
      await Clipboard.setData(ClipboardData(text: c.link));
      if (context.mounted) ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text(T.s('copied'))));
    }
    return;
  }
  final f = c.file;
  if (f == null) return;
  final dir = await getTemporaryDirectory();
  final file = File('${dir.path}/${f['File'] ?? f['file'] ?? '${c.id}.txt'}');
  await file.writeAsString((f['Body'] ?? f['body'] ?? '') as String);
  await SharePlus.instance.share(ShareParams(files: [XFile(file.path)], title: (f['Name'] ?? f['name'] ?? c.name) as String));
}

void _details(BuildContext context, Config c) {
  showXSheet<void>(
    context,
    title: c.name,
    builder: (ctx, _) => Column(crossAxisAlignment: CrossAxisAlignment.stretch, children: [
      Text('${c.label} · ${c.engineLabel}', style: Type.secondary),
      if (c.link.isNotEmpty) ...[
        const SizedBox(height: Space.l),
        Center(
          child: Container(
            padding: const EdgeInsets.all(Space.s),
            decoration: BoxDecoration(color: Colors.white, borderRadius: BorderRadius.circular(Radii.m)),
            child: QrImageView(data: c.link, size: 220),
          ),
        ),
        const SizedBox(height: Space.m),
        Row(children: [
          Expanded(
            child: OutlinedButton.icon(
              onPressed: () async {
                await Clipboard.setData(ClipboardData(text: c.link));
                if (ctx.mounted) Navigator.pop(ctx);
              },
              icon: const Icon(Icons.copy_rounded),
              label: Text(T.s('copy')),
            ),
          ),
          const SizedBox(width: Space.s),
          Expanded(
            child: OutlinedButton.icon(
              onPressed: () => SharePlus.instance.share(ShareParams(text: c.link)),
              icon: const Icon(Icons.share_rounded),
              label: Text(T.s('share')),
            ),
          ),
        ]),
      ],
    ]),
  );
}
