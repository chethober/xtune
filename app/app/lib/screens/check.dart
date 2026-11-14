import 'package:material_ui/material_ui.dart';

import '../main.dart';
import '../ui/glass.dart';
import '../ui/i18n.dart';

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
      int rank(Map r) => switch (r['state']) {
        'ok' => 0,
        'slow' || 'throttled' || 'flaky' => 1,
        'testing' => 2,
        'blocked' => 3,
        _ => 4,
      };
      final c = rank(a).compareTo(rank(b));
      if (c != 0) return c;
      return ((a['warmMs'] as num?) ?? 99999).compareTo((b['warmMs'] as num?) ?? 99999);
    });

    // the summary and its buttons: beside the results on wide screens, around them on phones
    final wide = twoPane(context);
    final top = <Widget>[
      // on mobile data with the data saver on, the check skips its downloads
      FutureBuilder<bool>(
        future: s.savingData(),
        builder: (_, snap) => snap.data == true
            ? Padding(
                padding: const EdgeInsets.only(bottom: Space.m),
                child: Notice(T.s('dataSaverOn'), color: X.blue, icon: Icons.data_saver_on_rounded),
              )
            : const SizedBox.shrink(),
      ),
      if (check != null)
        Glass(
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Text(running ? T.s('testing', {'n': finished, 'total': tested.length}) : T.s('done', {'n': tested.length}), style: Type.bodyStrong),
              const SizedBox(height: Space.m),
              Bar(fraction: tested.isEmpty ? 1 : finished / tested.length),
              const SizedBox(height: Space.m),
              Row(
                children: [
                  Expanded(child: _Count(ok, T.s('work'), X.accent)),
                  Expanded(child: _Count(warn, T.s('throttled'), X.warn)),
                  Expanded(child: _Count(bad, T.s('blocked'), X.bad)),
                ],
              ),
              if (s.reportMessage != null) ...[const SizedBox(height: Space.s), Text(s.reportMessage!, style: Type.caption)],
            ],
          ),
        ),
    ];
    final list = <Widget>[
      if (wide && results.isEmpty) Glass(child: Text(T.s('checkEmpty'), style: Type.secondary)),
      if (results.isNotEmpty) SectionLabel(T.s('resultsSoFar')),
      for (final r in results)
        Container(
          padding: const EdgeInsets.symmetric(vertical: 11),
          decoration: BoxDecoration(
            border: Border(bottom: BorderSide(color: X.track)),
          ),
          child: Row(
            children: [
              AnimatedSwitcher(
                duration: Motion.normal,
                child: Badge2(badge(r['state'] as String).$1, badge(r['state'] as String).$2, key: ValueKey(r['state'])),
              ),
              const SizedBox(width: Space.m),
              Expanded(
                flex: 3,
                child: Text(r['name'] as String? ?? '', style: Type.body, overflow: TextOverflow.ellipsis),
              ),
              const SizedBox(width: Space.s),
              Flexible(
                flex: 2,
                child: Text(_detail(r), textAlign: TextAlign.end, maxLines: 1, overflow: TextOverflow.ellipsis, style: Type.secondary),
              ),
            ],
          ),
        ),
    ];
    final buttons = <Widget>[
      const SizedBox(height: Space.l),
      CheckboxListTile(
        contentPadding: EdgeInsets.zero,
        controlAffinity: ListTileControlAffinity.leading,
        value: s.settings.shareResults,
        onChanged: (v) => s.updateSettings((x) => x.shareResults = v ?? true),
        title: Text(T.s('shareResults'), style: Type.secondary),
      ),
      const SizedBox(height: Space.s),
      SizedBox(
        height: 52,
        child: running
            ? OutlinedButton(onPressed: s.stopCheck, child: Text(T.s('stop')))
            : FilledButton(onPressed: s.sub == null ? null : s.startCheck, child: Text(check == null ? T.s('checkStart') : T.s('checkAgain'))),
      ),
    ];
    return XPage(
      title: T.s('checkTitle'),
      subtitle: T.s('checkHint', {'net': net}),
      side: wide ? [...top, ...buttons] : const [],
      children: wide ? list : [...top, ...list, ...buttons],
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
  Widget build(BuildContext context) => Column(
    crossAxisAlignment: CrossAxisAlignment.start,
    children: [
      TweenAnimationBuilder<int>(
        tween: IntTween(end: n),
        duration: Motion.normal,
        builder: (_, v, _) => Text('$v', style: Type.stat.copyWith(fontSize: 20, color: color)),
      ),
      Text(label, style: Type.caption),
    ],
  );
}
