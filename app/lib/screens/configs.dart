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
import 'options.dart';

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
  String? focus; // wide screens: the config shown beside the list
  static const groups = ['', 'Xray', 'VPN', 'Tunnels'];
  static const sorts = ['recommended', 'latency', 'name', 'type'];

  List<_Row> _rows(AppState s) {
    final all = s.sub?.allConfigs ?? [];
    // filters: all, Xray, VPN, Tunnels, starred, hidden
    final configs = filter == 5 ? all.where((c) => c.hidden).toList() : all.where((c) => !c.hidden).toList();
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
      if (filter >= 1 && filter <= 3 && c.group != groups[filter]) continue;
      if (filter == 4 && !c.star) continue;
      var state = 'unknown';
      int? ms;
      final delay = s.connected ? s.delayOf(c.id) : null;
      final sp = s.speed[c.id];
      final r = sp != null && sp['state'] != 'testing' ? sp : checked[c.id];
      if (c.handoff) {
        state = 'ext';
      } else if (delay != null) {
        state = delay < 0 ? 'bad' : 'ok';
        ms = delay < 0 ? null : delay;
      } else if (r != null) {
        state = switch (r['state']) {
          'ok' => 'ok',
          'blocked' => 'bad',
          _ => 'warn',
        };
        final w = (r['warmMs'] as num?)?.toInt() ?? -1;
        ms = w > 0 ? w : null;
      }
      if (sp?['state'] == 'testing') state = 'testing';
      rows.add(_Row(c, state, ms, i));
    }
    int rank(_Row r) => switch (r.state) {
      'ok' => 0,
      'warn' => 1,
      'unknown' => 2,
      'ext' => 3,
      _ => 4,
    };
    // starred first, then the chosen order
    rows.sort((a, b) => (a.c.star ? 0 : 1).compareTo(b.c.star ? 0 : 1));
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
    final page = XPage(
      title: T.s('configs'),
      subtitle: sub == null
          ? null
          : [
              if (sub.network != null) T.s('rankedFor', {'net': sub.network!}),
              T.s('updated', {'when': _ago(sub.fetched)}),
            ].join(' · '),
      onRefresh: () async {
        if (sub == null) return;
        try {
          await s.refreshSub(sub);
        } catch (e) {
          if (context.mounted) ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text('$e')));
        }
      },
      actions: [
        XMenuButton<String>(
          tooltip: T.s('sort'),
          icon: Icons.sort_rounded,
          selected: s.settings.sort,
          onSelected: (v) => s.updateSettings((x) => x.sort = v),
          items: [for (final k in sorts) XMenuItem(k, T.s('sort${k[0].toUpperCase()}${k.substring(1)}'))],
        ),
        if (sub != null) RoundIconButton(icon: Icons.low_priority_rounded, tooltip: T.s('autoPool'), onPressed: () => _editPool(context)),
        RoundIconButton(icon: Icons.add_rounded, tooltip: T.s('addSub'), filled: true, onPressed: () => showAddSub(context)),
      ],
      children: [
        Pills(
          labels: [
            T.s('all'),
            'Xray',
            'VPN',
            'Tunnels',
            '★ ${T.s('starred')}',
            if ((sub?.allConfigs ?? []).any((c) => c.hidden)) T.s('hiddenN', {'n': sub!.allConfigs.where((c) => c.hidden).length}),
          ],
          selected: filter,
          onSelect: (i) => setState(() => filter = i),
        ),
        const SizedBox(height: Space.m),
        if (sub != null)
          _ConfigRow(
            title: T.s('auto'),
            meta: sub.pool.isEmpty ? T.s('bestHere') : T.s('poolOf', {'n': sub.pool.length}),
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
              title: r.c.star ? '★ ${r.c.name}' : r.c.name,
              meta: switch (r.state) {
                'testing' => '${r.c.label} · ${T.s('speedTesting')}',
                'ext' => '${r.c.label} · ${T.s('runsElsewhere')}',
                'bad' => '${r.c.label} · ${T.s('blockedHere')}',
                _ => r.c.label,
              },
              trailing: switch (r.state) {
                'testing' => '…',
                'ext' => T.s('openInApp'),
                'bad' => '—',
                _ => r.ms == null ? '' : '${r.ms} ms',
              },
              engine: r.c.engineLabel,
              dot: X.status(r.state),
              selected: s.settings.selected == r.c.id,
              onTap: () {
                if (twoPane(context)) setState(() => focus = r.c.id);
                r.c.handoff ? _handoff(context, r.c) : s.select(r.c.id);
              },
              onLongPress: () => twoPane(context) ? setState(() => focus = r.c.id) : _details(context, r.c),
            ),
          ),
      ],
    );
    if (!twoPane(context)) return page;
    // wide: the details of the config tapped (or the one in use) beside the list
    final all = sub?.allConfigs ?? [];
    final shown = all.where((c) => c.id == focus).firstOrNull ?? s.activeConfig ?? all.where((c) => c.id == s.settings.selected).firstOrNull;
    return Row(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Expanded(child: page),
        SizedBox(
          width: 400,
          child: Padding(
            padding: const EdgeInsets.fromLTRB(0, 96, Space.page, Space.page),
            child: shown == null
                ? Glass(child: Text(T.s('pickToSee'), style: Type.secondary))
                : Glass(
                    child: SingleChildScrollView(
                      child: StatefulBuilder(
                        builder: (ctx, setPane) => Column(
                          crossAxisAlignment: CrossAxisAlignment.stretch,
                          children: [
                            Text(shown.name, style: Type.title),
                            const SizedBox(height: Space.s),
                            configDetails(context, shown, setPane, close: () => setState(() => focus = null), alive: () => mounted),
                          ],
                        ),
                      ),
                    ),
                  ),
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
  const _ConfigRow({
    required this.title,
    required this.meta,
    required this.trailing,
    required this.engine,
    required this.dot,
    required this.selected,
    required this.onTap,
    this.onLongPress,
  });
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
            child: Row(
              children: [
                Dot(dot, size: 10),
                const SizedBox(width: Space.m),
                Expanded(
                  child: Column(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      Text(title, style: Type.bodyStrong, overflow: TextOverflow.ellipsis),
                      const SizedBox(height: 3),
                      Text(meta, style: Type.caption, overflow: TextOverflow.ellipsis),
                    ],
                  ),
                ),
                const SizedBox(width: Space.s),
                Column(
                  crossAxisAlignment: CrossAxisAlignment.end,
                  children: [
                    Text(trailing, style: Type.number.copyWith(color: dot == X.bad ? X.bad : (dot == X.blue ? X.blue : X.text))),
                    if (engine.isNotEmpty) ...[const SizedBox(height: 3), Text(engine, style: Type.caption)],
                  ],
                ),
              ],
            ),
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

/// A config's sheet: speed test, star, rename, its own tweaks, hide, and its link as a QR code.
void _details(BuildContext context, Config c) {
  showXSheet<void>(
    context,
    title: c.name,
    builder: (ctx, setState) => configDetails(
      context,
      c,
      setState,
      close: () {
        if (ctx.mounted) Navigator.pop(ctx);
      },
      alive: () => ctx.mounted,
    ),
  );
}

/// A config's details and actions: in a sheet on phones, beside the list on wide screens.
Widget configDetails(BuildContext context, Config c, StateSetter setState, {required VoidCallback close, required bool Function() alive}) {
  final s = AppScope.of(context);
  final sp = s.speed[c.id];
  final m = s.sub?.meta[c.id];
  return Column(
    crossAxisAlignment: CrossAxisAlignment.stretch,
    children: [
      Text('${c.label} · ${c.engineLabel}${c.name != c.serverName ? ' · ${c.serverName}' : ''}', style: Type.secondary),
      const SizedBox(height: Space.m),
      if (!c.handoff)
        Glass(
          padding: const EdgeInsets.all(Space.m),
          child: Row(
            children: [
              Expanded(
                child: Text(
                  sp == null
                      ? T.s('speedHint')
                      : sp['state'] == 'testing'
                      ? T.s('speedTesting')
                      : _speedText(sp),
                  style: sp == null ? Type.secondary : Type.body,
                ),
              ),
              const SizedBox(width: Space.s),
              FilledButton.icon(
                style: FilledButton.styleFrom(minimumSize: const Size(0, 40)),
                onPressed: sp?['state'] == 'testing'
                    ? null
                    : () async {
                        final f = s.speedTest(c.id);
                        setState(() {});
                        await f;
                        if (alive()) setState(() {});
                      },
                icon: const Icon(Icons.speed_rounded, size: 18),
                label: Text(T.s('speedTest')),
              ),
            ],
          ),
        ),
      const SizedBox(height: Space.s),
      ListTile(
        contentPadding: EdgeInsets.zero,
        leading: Icon(c.star ? Icons.star_rounded : Icons.star_outline_rounded, color: c.star ? X.warn : null),
        title: Text(c.star ? T.s('unstar') : T.s('star')),
        onTap: () async {
          await s.markConfig(c.id, (m) => m.star = !c.star);
          close();
        },
      ),
      ListTile(
        contentPadding: EdgeInsets.zero,
        leading: const Icon(Icons.edit_rounded),
        title: Text(T.s('rename')),
        onTap: () async {
          close();
          final ctl = TextEditingController(text: c.name);
          final v = await showDialog<String>(
            context: context,
            builder: (d) => AlertDialog(
              title: Text(T.s('rename')),
              content: TextField(controller: ctl, autofocus: true),
              actions: [
                TextButton(onPressed: () => Navigator.pop(d, ''), child: Text(T.s('reset'))),
                FilledButton(onPressed: () => Navigator.pop(d, ctl.text.trim()), child: const Text('OK')),
              ],
            ),
          );
          if (v != null) await s.markConfig(c.id, (m) => m.rename = v == c.serverName ? '' : v);
        },
      ),
      if (!c.handoff)
        ListTile(
          contentPadding: EdgeInsets.zero,
          leading: const Icon(Icons.tune_rounded),
          title: Text(T.s('tweaksThis')),
          subtitle: Text(tweaksSummary(m?.tweaks ?? {})),
          onTap: () async {
            close();
            final t = await editTweaks(context, title: c.name, current: m?.tweaks ?? {}, perConfig: true);
            if (t != null) await s.setConfigTweaks(c.id, t);
          },
        ),
      ListTile(
        contentPadding: EdgeInsets.zero,
        leading: Icon(c.hidden ? Icons.visibility_rounded : Icons.visibility_off_rounded),
        title: Text(c.hidden ? T.s('unhide') : T.s('hide')),
        subtitle: c.hidden ? null : Text(T.s('hideHint')),
        onTap: () async {
          await s.markConfig(c.id, (m) => m.hidden = !c.hidden);
          close();
        },
      ),
      if (c.link.isNotEmpty) ...[
        const SizedBox(height: Space.m),
        Center(
          child: Container(
            padding: const EdgeInsets.all(Space.s),
            decoration: BoxDecoration(color: Colors.white, borderRadius: BorderRadius.circular(Radii.m)),
            child: QrImageView(data: c.link, size: 200),
          ),
        ),
        const SizedBox(height: Space.m),
        Row(
          children: [
            Expanded(
              child: OutlinedButton.icon(
                onPressed: () async {
                  await Clipboard.setData(ClipboardData(text: c.link));
                  close();
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
          ],
        ),
      ],
    ],
  );
}

String _speedText(Map<String, dynamic> r) {
  final st = r['state'] as String? ?? '';
  if (st == 'blocked') return T.s('speedBlocked');
  final warm = (r['warmMs'] as num?)?.toInt() ?? -1;
  final down = (r['downMbps'] as num?)?.toDouble() ?? 0;
  return [if (warm > 0) '$warm ms', if (down > 0) '${down.toStringAsFixed(1)} Mbps', if (st == 'throttled') T.s('throttled')].join(' · ');
}

/// The Auto pool: which configs Auto may use, in priority order (the first is the fallback).
Future<void> _editPool(BuildContext context) async {
  final s = AppScope.of(context);
  final sub = s.sub;
  if (sub == null) return;
  final runnable = sub.allConfigs.where((c) => !c.handoff).toList();
  final ids = sub.pool.isEmpty
      ? [
          for (final c in runnable)
            if (!c.dnstt) c.id,
        ]
      : [...sub.pool.where((id) => runnable.any((c) => c.id == id))];
  final rest = runnable.where((c) => !ids.contains(c.id)).map((c) => c.id).toList();
  final order = [...ids, ...rest];
  final on = {...ids};
  final names = {for (final c in runnable) c.id: c.name};
  final saved = await showXSheet<bool>(
    context,
    title: T.s('autoPool'),
    padded: false,
    builder: (ctx, setState) => Column(
      mainAxisSize: MainAxisSize.min,
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Padding(
          padding: const EdgeInsets.fromLTRB(Space.l, 0, Space.l, Space.s),
          child: Text(T.s('autoPoolHint'), style: Type.secondary),
        ),
        ReorderableListView(
          shrinkWrap: true,
          physics: const NeverScrollableScrollPhysics(),
          buildDefaultDragHandles: false,
          onReorderItem: (a, b) => setState(() {
            final id = order.removeAt(a);
            order.insert(b, id);
          }),
          children: [
            for (var i = 0; i < order.length; i++)
              CheckboxListTile(
                key: ValueKey(order[i]),
                value: on.contains(order[i]),
                onChanged: (v) => setState(() => v == true ? on.add(order[i]) : on.remove(order[i])),
                title: Text(names[order[i]] ?? order[i]),
                subtitle: i == order.indexWhere(on.contains) ? Text(T.s('poolFirst')) : null,
                secondary: ReorderableDragStartListener(index: i, child: const Icon(Icons.drag_indicator_rounded)),
              ),
          ],
        ),
        Padding(
          padding: const EdgeInsets.all(Space.l),
          child: Row(
            children: [
              TextButton(onPressed: () => Navigator.pop(ctx, false), child: Text(T.s('poolAll'))),
              const Spacer(),
              FilledButton(onPressed: on.isEmpty ? null : () => Navigator.pop(ctx, true), child: Text(T.s('save'))),
            ],
          ),
        ),
      ],
    ),
  );
  if (saved == null) return;
  await s.setPool(saved ? order.where(on.contains).toList() : []);
}
