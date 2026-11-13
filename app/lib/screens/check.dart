import 'package:material_ui/material_ui.dart';

import '../main.dart';
import '../ui/glass.dart';
import '../ui/i18n.dart';
import 'home.dart' show bottomPad;

class CheckScreen extends StatelessWidget {
  const CheckScreen({super.key});

  static (String, Color) badge(String state) => switch (state) {
        'ok' => ('WORKS', X.accent),
        'slow' => ('SLOW', X.warn),
        'throttled' => ('THROTTLED', X.warn),
        'flaky' => ('FLAKY', X.warn),
        'blocked' => ('BLOCKED', X.bad),
        'skipped' => ('OTHER APP', X.muted),
        _ => ('TESTING', X.blue),
      };

  @override
  Widget build(BuildContext context) {
    final s = AppScope.of(context);
    final check = s.check;
    final results = ((check?['results'] as List?) ?? []).map((r) => Map<String, dynamic>.from(r as Map)).toList();
    final running = check != null && check['done'] != true;
    final tested = results.where((r) => r['state'] != 'skipped').toList();
    final finished = tested.where((r) => r['state'] != 'testing').length;
    final ok = tested.where((r) => r['state'] == 'ok').length;
    final warn = tested.where((r) => ['slow', 'throttled', 'flaky'].contains(r['state'])).length;
    final bad = tested.where((r) => r['state'] == 'blocked').length;
    final net = (s.network?['name'] as String?)?.isNotEmpty == true ? s.network!['name'] as String : (T.rtl ? 'این شبکه' : 'this network');
    results.sort((a, b) {
      int rank(Map r) => switch (r['state']) { 'ok' => 0, 'slow' || 'throttled' || 'flaky' => 1, 'testing' => 2, 'blocked' => 3, _ => 4 };
      final c = rank(a).compareTo(rank(b));
      if (c != 0) return c;
      return ((a['warmMs'] as num?) ?? 99999).compareTo((b['warmMs'] as num?) ?? 99999);
    });

    return ListView(
      padding: const EdgeInsets.only(bottom: bottomPad),
      children: [
        ScreenTitle(T.s('checkTitle'), subtitle: T.s('checkHint', {'net': net})),
        if (check != null)
          Padding(
            padding: const EdgeInsets.fromLTRB(20, 8, 20, 0),
            child: Glass(
              child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
                Text(
                  running ? T.s('testing', {'n': finished, 'total': tested.length}) : T.s('done', {'n': tested.length}),
                  style: const TextStyle(fontSize: 15, fontWeight: FontWeight.w600),
                ),
                const SizedBox(height: 12),
                Bar(fraction: tested.isEmpty ? 1 : finished / tested.length),
                const SizedBox(height: 12),
                Row(children: [
                  Expanded(child: _Count(ok, T.s('work'), X.accent)),
                  Expanded(child: _Count(warn, T.s('throttled'), X.warn)),
                  Expanded(child: _Count(bad, T.s('blocked'), X.bad)),
                ]),
                if (s.reportMessage != null) ...[
                  const SizedBox(height: 10),
                  Text(s.reportMessage!, style: const TextStyle(fontSize: 12, color: X.muted)),
                ],
              ]),
            ),
          ),
        if (results.isNotEmpty) Padding(padding: const EdgeInsets.symmetric(horizontal: 16), child: SectionLabel(T.s('resultsSoFar'))),
        for (final r in results)
          Padding(
            padding: const EdgeInsets.symmetric(horizontal: 20),
            child: Container(
              padding: const EdgeInsets.symmetric(vertical: 11),
              decoration: const BoxDecoration(border: Border(bottom: BorderSide(color: X.track))),
              child: Row(children: [
                Badge2(badge(r['state'] as String).$1, badge(r['state'] as String).$2),
                const SizedBox(width: 12),
                Expanded(
                    flex: 3,
                    child: Text(r['name'] as String? ?? '', style: const TextStyle(fontWeight: FontWeight.w500), overflow: TextOverflow.ellipsis)),
                const SizedBox(width: 8),
                Flexible(
                    flex: 2,
                    child: Text(_detail(r),
                        textAlign: TextAlign.end, maxLines: 1, overflow: TextOverflow.ellipsis, style: const TextStyle(fontSize: 13, color: X.muted))),
              ]),
            ),
          ),
        Padding(
          padding: const EdgeInsets.fromLTRB(20, 16, 20, 0),
          child: Column(children: [
            CheckboxListTile(
              contentPadding: EdgeInsets.zero,
              controlAffinity: ListTileControlAffinity.leading,
              value: s.settings.shareResults,
              onChanged: (v) => s.updateSettings((x) => x.shareResults = v ?? true),
              title: Text(T.s('shareResults'), style: const TextStyle(fontSize: 13, color: X.muted)),
            ),
            const SizedBox(height: 8),
            SizedBox(
              width: double.infinity,
              height: 52,
              child: running
                  ? OutlinedButton(
                      style: OutlinedButton.styleFrom(foregroundColor: X.text, side: const BorderSide(color: X.edge), backgroundColor: X.glass),
                      onPressed: s.stopCheck,
                      child: Text(T.s('stop')))
                  : FilledButton(
                      style: FilledButton.styleFrom(backgroundColor: X.accent, foregroundColor: X.bg),
                      onPressed: s.sub == null ? null : s.startCheck,
                      child: Text(check == null ? T.s('checkStart') : T.s('checkAgain'))),
            ),
          ]),
        ),
      ],
    );
  }

  static String _detail(Map<String, dynamic> r) {
    final st = r['state'] as String? ?? '';
    final warm = (r['warmMs'] as num?)?.toInt() ?? -1;
    final down = (r['downMbps'] as num?)?.toDouble() ?? 0;
    final early = (r['earlyMbps'] as num?)?.toDouble() ?? 0;
    final late = (r['lateMbps'] as num?)?.toDouble() ?? 0;
    switch (st) {
      case 'testing':
        return '…';
      case 'skipped':
        return '';
      case 'blocked':
        final d = (r['detail'] as String? ?? '').toLowerCase();
        if (d.contains('reset')) return 'reset by the network';
        if (d.contains('timeout') || d.contains('deadline')) return 'no answer';
        if (d.contains('refused')) return 'refused';
        if (d.contains('eof')) return 'cut off';
        return 'no answer';
      case 'throttled':
        return '${early.toStringAsFixed(1)} → ${late.toStringAsFixed(1)} Mbps';
    }
    return [if (warm > 0) '$warm ms', if (down > 0) '${(down / 8).toStringAsFixed(1)} MB/s'].join(' · ');
  }
}

class _Count extends StatelessWidget {
  const _Count(this.n, this.label, this.color);
  final int n;
  final String label;
  final Color color;
  @override
  Widget build(BuildContext context) => Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
        Text('$n', style: TextStyle(fontSize: 20, fontWeight: FontWeight.w600, color: color)),
        Text(label, style: const TextStyle(fontSize: 12, color: X.muted)),
      ]);
}
