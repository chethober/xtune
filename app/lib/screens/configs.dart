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
import 'home.dart' show bottomPad;

class ConfigsScreen extends StatefulWidget {
  const ConfigsScreen({super.key});
  @override
  State<ConfigsScreen> createState() => _ConfigsScreenState();
}

class _ConfigsScreenState extends State<ConfigsScreen> {
  int filter = 0;
  static const groups = ['', 'Xray', 'VPN', 'Tunnels'];

  @override
  Widget build(BuildContext context) {
    final s = AppScope.of(context);
    final sub = s.sub;
    final configs = (sub?.configs ?? []).where((c) => filter == 0 || c.group == groups[filter]).toList();
    final ago = sub == null ? '' : _ago(sub.fetched);
    return RefreshIndicator(
      onRefresh: () async {
        if (sub != null) {
          try {
            await s.refreshSub(sub);
          } catch (e) {
            if (context.mounted) ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text('$e')));
          }
        }
      },
      child: ListView(
        padding: const EdgeInsets.only(bottom: bottomPad),
        children: [
          ScreenTitle(
            T.s('configs'),
            subtitle: sub == null
                ? null
                : [if (sub.network != null) T.s('rankedFor', {'net': sub.network!}), T.s('updated', {'when': ago})].join(' · '),
            actions: [RoundIconButton(icon: Icons.add_rounded, tooltip: T.s('addSub'), filled: true, onPressed: () => showAddSub(context))],
          ),
          Padding(
            padding: const EdgeInsets.fromLTRB(20, 4, 20, 12),
            child: Pills(
              labels: [T.s('all'), 'Xray', 'VPN', 'Tunnels'],
              selected: filter,
              onSelect: (i) => setState(() => filter = i),
            ),
          ),
          if (sub != null)
            Padding(
              padding: const EdgeInsets.symmetric(horizontal: 20),
              child: _ConfigRow(
                title: T.s('auto'),
                meta: T.s('bestHere'),
                trailing: '',
                engine: '',
                dot: X.accent,
                selected: s.settings.selected == null,
                onTap: () => s.select(null),
              ),
            ),
          for (final c in configs)
            Padding(
              padding: const EdgeInsets.fromLTRB(20, 8, 20, 0),
              child: _row(context, s, c),
            ),
        ],
      ),
    );
  }

  Widget _row(BuildContext context, AppState s, Config c) {
    final checked = _checkResult(s, c);
    final delay = s.connected ? s.delayOf(c.id) : null;
    Color dot = X.muted;
    String ping = '';
    String meta = c.label;
    if (c.handoff) {
      dot = X.blue;
      ping = T.s('openInApp');
      meta = '${c.label} · ${T.s('runsElsewhere')}';
    } else if (delay != null) {
      dot = delay < 0 ? X.bad : X.accent;
      ping = delay < 0 ? '—' : '$delay ms';
    } else if (checked != null) {
      final st = checked['state'] as String? ?? '';
      dot = switch (st) { 'ok' => X.accent, 'slow' || 'throttled' || 'flaky' => X.warn, 'blocked' => X.bad, _ => X.muted };
      final warm = (checked['warmMs'] as num?)?.toInt() ?? -1;
      ping = st == 'blocked' ? '—' : (warm > 0 ? '$warm ms' : '');
      if (st == 'blocked') meta = '${c.label} · ${T.s('blockedHere')}';
    }
    return _ConfigRow(
      title: c.name,
      meta: meta,
      trailing: ping,
      engine: c.engineLabel,
      dot: dot,
      selected: s.settings.selected == c.id,
      onTap: () => c.handoff ? _handoff(context, c) : s.select(c.id),
      onLongPress: () => _details(context, c),
    );
  }

  Map<String, dynamic>? _checkResult(AppState s, Config c) {
    if (s.check == null || s.checkSubId != s.sub?.id) return null;
    for (final r in (s.check!['results'] as List? ?? [])) {
      final m = Map<String, dynamic>.from(r as Map);
      if (m['id'] == c.id && m['state'] != 'testing') return m;
    }
    return null;
  }
}

String _ago(DateTime t) {
  final d = DateTime.now().difference(t);
  if (d.inMinutes < 1) return 'just now';
  if (d.inHours < 1) return '${d.inMinutes} min ago';
  if (d.inDays < 1) return '${d.inHours} h ago';
  return '${d.inDays} d ago';
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
        child: Glass(
          radius: 18,
          padding: const EdgeInsets.symmetric(horizontal: 14, vertical: 12),
          color: selected ? X.accent.withValues(alpha: 0.14) : null,
          border: selected ? X.accent.withValues(alpha: 0.45) : null,
          onTap: onTap,
          child: Row(children: [
            Container(width: 10, height: 10, decoration: BoxDecoration(color: dot, shape: BoxShape.circle)),
            const SizedBox(width: 12),
            Expanded(
              child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
                Text(title, style: const TextStyle(fontSize: 15, fontWeight: FontWeight.w600), overflow: TextOverflow.ellipsis),
                const SizedBox(height: 3),
                Text(meta, style: const TextStyle(fontSize: 12, color: X.muted), overflow: TextOverflow.ellipsis),
              ]),
            ),
            const SizedBox(width: 8),
            Column(crossAxisAlignment: CrossAxisAlignment.end, children: [
              Text(trailing, style: TextStyle(fontSize: 14, fontWeight: FontWeight.w600, color: dot == X.bad ? X.bad : (dot == X.blue ? X.blue : X.text))),
              if (engine.isNotEmpty) ...[
                const SizedBox(height: 3),
                Text(engine, style: const TextStyle(fontSize: 12, color: X.muted)),
              ],
            ]),
          ]),
        ),
      ),
    );
  }
}

/// Configs another app runs: Telegram opens its proxy link; VPN profiles are saved and shared.
Future<void> _handoff(BuildContext context, Config c) async {
  if (c.kind == 'mtproto' && c.link.isNotEmpty) {
    final u = Uri.parse(c.link);
    if (!await launchUrl(u, mode: LaunchMode.externalApplication) && context.mounted) {
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
  showModalBottomSheet<void>(
    context: context,
    backgroundColor: Colors.transparent,
    isScrollControlled: true,
    builder: (ctx) => Padding(
      padding: const EdgeInsets.all(12),
      child: Glass(
        color: const Color(0xE6151B24),
        padding: const EdgeInsets.all(20),
        child: Column(mainAxisSize: MainAxisSize.min, crossAxisAlignment: CrossAxisAlignment.stretch, children: [
          Text(c.name, style: const TextStyle(fontSize: 18, fontWeight: FontWeight.w600)),
          Text('${c.label} · ${c.engineLabel}', style: const TextStyle(color: X.muted)),
          if (c.link.isNotEmpty) ...[
            const SizedBox(height: 16),
            Center(
              child: Container(
                color: Colors.white,
                padding: const EdgeInsets.all(10),
                child: QrImageView(data: c.link, size: 220),
              ),
            ),
            const SizedBox(height: 12),
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
              const SizedBox(width: 8),
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
      ),
    ),
  );
}
