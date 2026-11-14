import 'dart:async';
import 'dart:io';

import 'package:material_ui/material_ui.dart';

import '../main.dart';
import '../state/models.dart';
import '../ui/glass.dart';
import '../ui/i18n.dart';

/// The protocols a tune can add, as xtune names them (--protocols), with the kinds that show one
/// is deployed. Plain Xray configs are always tuned.
const tuneProtocols = <String, List<String>>{
  'ssh': ['ssh', 'ssh-tls', 'cdn-ssh'],
  'dnstt': ['dnstt'],
  'mtproto': ['mtproto'],
  'awg': ['amneziawg'],
  'trojan': ['trojan-reality'],
  'worker': ['worker-ws'],
};

const _icons = <String, IconData>{
  'ssh': Icons.terminal_rounded,
  'dnstt': Icons.dns_rounded,
  'mtproto': Icons.send_rounded,
  'awg': Icons.vpn_lock_rounded,
  'trojan': Icons.shield_moon_outlined,
  'worker': Icons.cloud_outlined,
};

/// Tune a server from this device: the server starts test configs, this device measures each from
/// its own network, and the best are deployed. Users and their links are kept.
class TunePage extends StatefulWidget {
  const TunePage({super.key, required this.server, required this.overview, this.zone = ''});
  final ServerEntry server;
  final Map<String, dynamic> overview;

  /// A Cloudflare zone (from the domains wizard): the tune names its direct and CDN domains there.
  final String zone;
  @override
  State<TunePage> createState() => _TunePageState();
}

class _TunePageState extends State<TunePage> {
  late final Set<String> deployed = {
    for (final e in (widget.overview['entries'] as List? ?? []))
      for (final p in tuneProtocols.entries)
        if (p.value.contains((e as Map)['kind'])) p.key,
  };
  late final Set<String> chosen = {...deployed};
  late final dnsZone = TextEditingController(text: widget.overview['dnsDomain'] as String? ?? '');
  late final bool handoffDeployed = (widget.overview['entries'] as List? ?? []).any(
    (e) => ['openvpn-udp', 'openvpn-tcp', 'ikev2'].contains((e as Map)['kind']),
  );
  String depth = 'smart';
  int keep = 3;
  Map<String, dynamic>? job;
  String? error;
  Timer? poll;

  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addPostFrameCallback((_) => _resume());
  }

  @override
  void dispose() {
    poll?.cancel();
    super.dispose();
  }

  String get phase => job?['phase'] as String? ?? '';
  bool get running => const ['preparing', 'measuring', 'deploying'].contains(phase);

  /// A tune started earlier (the page was closed while it ran) is picked up again.
  Future<void> _resume() async {
    try {
      final j = await AppScope.of(context).core.call('manage.tune.status', {'host': widget.server.host});
      final m = Map<String, dynamic>.from(j as Map);
      if ((m['phase'] as String? ?? '').isNotEmpty && m['phase'] != 'cancelled' && mounted) {
        setState(() => job = m);
        _watch();
      }
    } catch (_) {}
  }

  void _watch() {
    poll?.cancel();
    if (!running) return;
    poll = Timer.periodic(const Duration(milliseconds: 700), (_) async {
      final id = job?['id'];
      if (id == null || !mounted) return;
      try {
        final j = await AppScope.of(context).core.call('manage.tune.status', {'id': id});
        if (!mounted) return;
        setState(() => job = Map<String, dynamic>.from(j as Map));
        if (!running) poll?.cancel();
      } catch (_) {}
    });
  }

  Future<void> _start() async {
    if (chosen.contains('dnstt') && dnsZone.text.trim().isEmpty) {
      setState(() => error = T.s('tuneDnsZoneNeeded'));
      return;
    }
    setState(() => error = null);
    final s = AppScope.of(context);
    try {
      final j = await s.manage('manage.tune.start', widget.server, {
        'options': {
          'protocols': chosen.toList(),
          'depth': depth,
          if (chosen.contains('dnstt')) 'dnsDomain': dnsZone.text.trim(),
          if (widget.zone.isNotEmpty) 'zone': widget.zone,
          'network': (s.network?['name'] as String?) ?? '',
        },
      });
      setState(() => job = Map<String, dynamic>.from(j as Map));
      _watch();
    } catch (e) {
      setState(() => error = '$e');
    }
  }

  Future<void> _deploy() async {
    try {
      final j = await AppScope.of(context).core.call('manage.tune.deploy', {'id': job!['id'], 'n': keep});
      setState(() => job = Map<String, dynamic>.from(j as Map));
      _watch();
    } catch (e) {
      setState(() => error = '$e');
    }
  }

  Future<void> _cancel() async {
    try {
      await AppScope.of(context).core.call('manage.tune.cancel', {'id': job!['id']});
    } catch (_) {}
    poll?.cancel();
    setState(() => job = null);
  }

  @override
  Widget build(BuildContext context) {
    return XPage(
      title: T.s('tune'),
      subtitle: widget.server.name.isNotEmpty ? widget.server.name : widget.server.host,
      children: [
        if (error != null && job != null) Notice(error!),
        if (!Platform.isAndroid && AppScope.of(context).connected && AppScope.of(context).settings.mode == 'vpn')
          Notice(T.s('tuneDisconnectFirst'), color: X.warn, icon: Icons.info_outline_rounded),
        if (job == null || phase == 'cancelled') ..._setup(context) else ..._progress(context),
      ],
    );
  }

  List<Widget> _setup(BuildContext context) => [
    if (widget.zone.isNotEmpty) ...[
      Notice(T.s('tuneWithZone', {'zone': widget.zone}), color: X.blue, icon: Icons.cloud_outlined),
      const SizedBox(height: Space.m),
    ],
    Glass(
      child: Row(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Icon(Icons.auto_fix_high_rounded, color: X.accent),
          const SizedBox(width: Space.m),
          Expanded(child: Text(T.s('tuneHow'), style: Type.secondary)),
        ],
      ),
    ),
    SectionLabel(T.s('protocols')),
    Group(
      children: [
        ListTile(
          leading: const Icon(Icons.bolt_rounded),
          title: const Text('Xray'),
          subtitle: Text(T.s('tuneXray')),
          trailing: Icon(Icons.check_rounded, color: X.accent),
        ),
        for (final p in tuneProtocols.keys)
          SwitchListTile(
            secondary: Icon(_icons[p]),
            title: Text(T.s('proto_$p')),
            subtitle: Text(deployed.contains(p) ? T.s('tuneDeployedNow') : T.s('proto_${p}_hint')),
            value: chosen.contains(p),
            onChanged: (v) => setState(() => v ? chosen.add(p) : chosen.remove(p)),
          ),
      ],
    ),
    if (chosen.contains('dnstt')) ...[
      const SizedBox(height: Space.m),
      TextField(
        controller: dnsZone,
        textDirection: TextDirection.ltr,
        keyboardType: TextInputType.url,
        decoration: InputDecoration(labelText: T.s('tuneDnsZone'), hintText: 't.example.com', helperText: T.s('tuneDnsZoneHint'), helperMaxLines: 3),
      ),
    ],
    if (handoffDeployed) ...[const SizedBox(height: Space.m), Notice(T.s('tuneHandoffWarn'), color: X.warn, icon: Icons.info_outline_rounded)],
    SectionLabel(T.s('tuneDepth')),
    Segmented(
      labels: [T.s('tuneSmart'), T.s('tuneDeep')],
      selected: depth == 'deep' ? 1 : 0,
      onSelect: (i) => setState(() => depth = i == 1 ? 'deep' : 'smart'),
    ),
    Padding(
      padding: const EdgeInsets.fromLTRB(Space.xs, Space.s, Space.xs, 0),
      child: Text(depth == 'deep' ? T.s('tuneDeepHint') : T.s('tuneSmartHint'), style: Type.caption),
    ),
    const SizedBox(height: Space.xl),
    if (error != null) ...[Notice(error!), const SizedBox(height: Space.m)],
    FilledButton.icon(
      style: FilledButton.styleFrom(minimumSize: const Size.fromHeight(52), backgroundColor: X.accent, foregroundColor: X.onAccent),
      onPressed: _start,
      icon: const Icon(Icons.play_arrow_rounded),
      label: Text(T.s('tuneStart')),
    ),
  ];

  List<Widget> _progress(BuildContext context) {
    final j = job!;
    final results = ((j['results'] as List?) ?? []).map((e) => Map<String, dynamic>.from(e as Map)).toList();
    final scores = ((j['scores'] as List?) ?? []).map((e) => (e as num).toDouble()).toList();
    final total = (j['total'] as num?)?.toInt() ?? 0;
    final done = (j['done'] as num?)?.toInt() ?? 0;
    final working = results.where((r) => (r['ok'] as num? ?? 0) > 0).length;
    final steps = ['preparing', 'measuring', 'measured', 'deploying', 'done'];
    final step = steps.indexOf(phase);
    return [
      Glass(
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            for (var i = 0; i < 3; i++)
              Padding(
                padding: const EdgeInsets.symmetric(vertical: 6),
                child: Row(
                  children: [
                    _StepDot(state: _rowState(i)),
                    const SizedBox(width: Space.m),
                    Expanded(child: Text(T.s(['tuneStep1', 'tuneStep2', 'tuneStep3'][i]), style: Type.body)),
                    if (i == 1 && total > 0) Text('$done / $total', style: Type.caption),
                  ],
                ),
              ),
            if (phase == 'measuring' && total > 0) ...[const SizedBox(height: Space.s), Bar(fraction: done / total)],
            if (phase == 'preparing') ...[const SizedBox(height: Space.s), Text(T.s('tunePreparingHint'), style: Type.caption)],
          ],
        ),
      ),
      if (phase == 'failed') ...[const SizedBox(height: Space.m), Notice(j['error'] as String? ?? 'failed')],
      if ((j['skipped'] as List?)?.isNotEmpty == true && step >= 1) ...[
        const SizedBox(height: Space.s),
        Text(T.s('tuneSkipped', {'n': (j['skipped'] as List).length}), style: Type.caption),
      ],
      if (results.isNotEmpty) ...[
        SectionLabel(phase == 'measuring' ? T.s('tuneMeasuring') : T.s('tuneResults', {'n': working, 'total': results.length})),
        Group(
          children: [
            for (var i = 0; i < results.length && i < (phase == 'measuring' ? 200 : 30); i++)
              _ResultRow(
                r: results[i],
                score: i < scores.length ? scores[i] : null,
                top: phase != 'measuring' && i < keep && (scores.length > i && scores[i] > 0),
              ),
          ],
        ),
      ],
      if (phase == 'measured') ...[
        SectionLabel(T.s('tuneKeep')),
        Segmented(labels: const ['1', '2', '3', '4', '5'], selected: keep - 1, onSelect: (i) => setState(() => keep = i + 1)),
        Padding(
          padding: const EdgeInsets.fromLTRB(Space.xs, Space.s, Space.xs, 0),
          child: Text(T.s('tuneKeepHint'), style: Type.caption),
        ),
        const SizedBox(height: Space.xl),
        FilledButton.icon(
          style: FilledButton.styleFrom(minimumSize: const Size.fromHeight(52), backgroundColor: X.accent, foregroundColor: X.onAccent),
          onPressed: working == 0 ? null : _deploy,
          icon: const Icon(Icons.rocket_launch_rounded),
          label: Text(working == 0 ? T.s('tuneNothingWorked') : T.s('tuneDeploy')),
        ),
      ],
      if (phase == 'done' && (j['output'] as String? ?? '').isNotEmpty) ...[
        SectionLabel(T.s('tuneDeployed')),
        Glass(
          padding: const EdgeInsets.all(Space.m),
          child: Directionality(
            textDirection: TextDirection.ltr,
            child: SelectableText((j['output'] as String).trim(), style: Type.mono.copyWith(fontSize: 12)),
          ),
        ),
      ],
      const SizedBox(height: Space.l),
      if (phase == 'done' || phase == 'failed')
        OutlinedButton(
          style: OutlinedButton.styleFrom(minimumSize: const Size.fromHeight(48)),
          onPressed: () => setState(() => job = null),
          child: Text(T.s('tuneAgain')),
        )
      else if (phase != 'deploying')
        TextButton(onPressed: _cancel, child: Text(T.s('tuneCancel'))),
    ];
  }

  /// Row i (prepare, measure, deploy): 1 done, 0 running, 2 waiting, -1 failed.
  int _rowState(int i) {
    const runs = ['preparing', 'measuring', 'deploying'];
    const order = ['preparing', 'measuring', 'measured', 'deploying', 'done'];
    if (phase == 'failed') {
      final at = runs.indexOf(job?['failed'] as String? ?? 'preparing');
      return i < at ? 1 : (i == at ? -1 : 2);
    }
    if (phase == runs[i]) return 0;
    final step = order.indexOf(phase);
    return step > order.indexOf(runs[i]) ? 1 : 2;
  }
}

/// A step's mark: 1 done, 0 running, 2 waiting, -1 failed.
class _StepDot extends StatelessWidget {
  const _StepDot({required this.state});
  final int state;
  @override
  Widget build(BuildContext context) => SizedBox(
    width: 22,
    height: 22,
    child: switch (state) {
      1 => Icon(Icons.check_circle_rounded, color: X.accent, size: 22),
      0 => Padding(
        padding: const EdgeInsets.all(3),
        child: CircularProgressIndicator(strokeWidth: 2.2, color: X.accent),
      ),
      -1 => Icon(Icons.error_rounded, color: X.bad, size: 22),
      _ => Icon(Icons.radio_button_unchecked_rounded, color: X.muted, size: 22),
    },
  );
}

class _ResultRow extends StatelessWidget {
  const _ResultRow({required this.r, required this.score, required this.top});
  final Map<String, dynamic> r;
  final double? score;
  final bool top;

  @override
  Widget build(BuildContext context) {
    final st = r['state'] as String? ?? 'testing';
    final color = switch (st) {
      'ok' => X.accent,
      'testing' => X.muted,
      'skipped' => X.muted,
      'blocked' => X.bad,
      _ => X.warn,
    };
    final down = (r['downMbps'] as num? ?? 0).toDouble();
    final warm = (r['warmMs'] as num? ?? -1).toInt();
    return ListTile(
      dense: true,
      leading: st == 'testing' ? SizedBox(width: 14, height: 14, child: CircularProgressIndicator(strokeWidth: 2, color: X.muted)) : Dot(color),
      minLeadingWidth: 16,
      title: Directionality(
        textDirection: TextDirection.ltr,
        child: Text(
          r['name'] as String? ?? '',
          style: Type.mono.copyWith(fontSize: 12.5, color: X.text),
          maxLines: 2,
          overflow: TextOverflow.ellipsis,
        ),
      ),
      subtitle: Text(
        st == 'testing'
            ? T.s('testing')
            : st == 'blocked' || st == 'skipped'
            ? (r['detail'] as String? ?? st)
            : [if (warm >= 0) '$warm ms', if (down > 0) '${down.toStringAsFixed(1)} Mbps', st].join(' · '),
        maxLines: 1,
        overflow: TextOverflow.ellipsis,
      ),
      trailing: score == null
          ? null
          : Row(
              mainAxisSize: MainAxisSize.min,
              children: [
                if (top) ...[Badge2(T.s('tuneTop'), X.accent), const SizedBox(width: Space.s)],
                SizedBox(
                  width: 36,
                  child: Text(score!.toStringAsFixed(1), style: Type.caption, textAlign: TextAlign.end),
                ),
              ],
            ),
    );
  }
}
