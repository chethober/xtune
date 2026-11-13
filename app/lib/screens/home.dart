import 'dart:async';

import 'package:material_ui/material_ui.dart';

import '../main.dart';
import '../state/app_state.dart';
import '../state/models.dart';
import '../ui/glass.dart';
import '../ui/i18n.dart';
import '../ui/power_button.dart';
import 'add_sub.dart';

class HomeScreen extends StatefulWidget {
  const HomeScreen({super.key, required this.onOpenConfigs});
  final VoidCallback onOpenConfigs;
  @override
  State<HomeScreen> createState() => _HomeScreenState();
}

class _HomeScreenState extends State<HomeScreen> {
  int _lastUp = 0, _lastDown = 0;
  double _upRate = 0, _downRate = 0;
  DateTime _lastAt = DateTime.now();
  Timer? _tick;

  @override
  void initState() {
    super.initState();
    _tick = Timer.periodic(const Duration(seconds: 1), (_) => _rates());
  }

  @override
  void dispose() {
    _tick?.cancel();
    super.dispose();
  }

  void _rates() {
    if (!mounted) return;
    final st = AppScope.of(context).status;
    final up = (st['up'] as num?)?.toInt() ?? 0, down = (st['down'] as num?)?.toInt() ?? 0;
    final now = DateTime.now();
    final secs = now.difference(_lastAt).inMilliseconds / 1000;
    if (secs > 0 && up >= _lastUp && down >= _lastDown) {
      setState(() {
        _upRate = (up - _lastUp) / secs;
        _downRate = (down - _lastDown) / secs;
      });
    }
    _lastUp = up;
    _lastDown = down;
    _lastAt = now;
  }

  @override
  Widget build(BuildContext context) {
    final s = AppScope.of(context);
    final sub = s.sub;
    return XPage(
      title: 'xtune',
      leading: Text('xtune', style: Type.wordmark),
      actions: [if (sub != null) _SubChip(state: s)],
      children: [
        if (s.network != null)
          Row(mainAxisAlignment: MainAxisAlignment.center, children: [
            const Icon(Icons.wifi_rounded, size: 14, color: X.muted),
            const SizedBox(width: 6),
            Flexible(child: Text('${s.network!['name'] ?? ''} · ${s.network!['asn'] ?? ''}', style: Type.secondary, overflow: TextOverflow.ellipsis)),
          ]),
        if (sub == null) ...[
          const SizedBox(height: 48),
          _EmptyState(onAdd: () => showAddSub(context)),
        ] else ...[
          const SizedBox(height: Space.l),
          Center(
            child: PowerButton(
              on: s.connected,
              busy: s.busy,
              label: s.connected ? T.s('disconnect') : T.s('connect'),
              onTap: s.busy ? null : s.toggle,
            ),
          ),
          const SizedBox(height: Space.s),
          _StatusText(state: s),
          const SizedBox(height: Space.xl),
          Segmented(
            labels: [T.s('auto'), T.s('pick')],
            selected: s.settings.selected == null ? 0 : 1,
            onSelect: (i) => i == 0 ? s.select(null) : widget.onOpenConfigs(),
          ),
          const SizedBox(height: Space.m),
          _UsingNow(state: s, up: _upRate, down: _downRate),
          const SizedBox(height: Space.m),
          _UsageCard(sub: sub),
        ],
        if (s.connected && (s.status['error'] as String?)?.isNotEmpty == true) ...[
          const SizedBox(height: Space.m),
          Notice(s.status['error'] as String, color: X.warn, icon: Icons.info_outline_rounded),
        ],
        if (s.error != null) ...[const SizedBox(height: Space.m), Notice(s.error!)],
      ],
    );
  }
}

class _SubChip extends StatelessWidget {
  const _SubChip({required this.state});
  final AppState state;

  @override
  Widget build(BuildContext context) {
    return Glass(
      radius: Radii.card,
      padding: EdgeInsets.zero,
      onTap: () => _pickSub(context, state),
      child: Padding(
        padding: const EdgeInsets.symmetric(horizontal: 14, vertical: 11),
        child: Row(mainAxisSize: MainAxisSize.min, children: [
          ConstrainedBox(
            constraints: const BoxConstraints(maxWidth: 190),
            child: Text(state.sub!.name, overflow: TextOverflow.ellipsis, style: Type.body.copyWith(fontSize: 14)),
          ),
          const SizedBox(width: 6),
          const Icon(Icons.expand_more_rounded, size: 18),
        ]),
      ),
    );
  }
}

void _pickSub(BuildContext context, AppState state) {
  showXSheet<void>(
    context,
    title: T.s('subscriptions'),
    padded: false,
    builder: (ctx, _) => Column(mainAxisSize: MainAxisSize.min, children: [
      for (final s in state.subs)
        ListTile(
          leading: Icon(s.id == state.sub?.id ? Icons.radio_button_checked : Icons.radio_button_off, color: s.id == state.sub?.id ? X.accent : X.muted),
          title: Text(s.name),
          subtitle: Text(s.user),
          onTap: () {
            Navigator.pop(ctx);
            state.selectSub(s);
          },
        ),
      ListTile(
        leading: const Icon(Icons.add_rounded),
        title: Text(T.s('addSub')),
        onTap: () {
          Navigator.pop(ctx);
          showAddSub(context);
        },
      ),
    ]),
  );
}

class _EmptyState extends StatelessWidget {
  const _EmptyState({required this.onAdd});
  final VoidCallback onAdd;

  @override
  Widget build(BuildContext context) {
    return Glass(
      padding: const EdgeInsets.all(Space.xxl),
      child: Column(children: [
        const Icon(Icons.link_rounded, size: 48, color: X.accent),
        const SizedBox(height: Space.m),
        Text(T.s('noSub'), style: Type.title),
        const SizedBox(height: Space.s),
        Text(T.s('noSubHint'), textAlign: TextAlign.center, style: Type.secondary),
        const SizedBox(height: Space.xl),
        FilledButton.icon(
          style: FilledButton.styleFrom(minimumSize: const Size(200, 52)),
          onPressed: onAdd,
          icon: const Icon(Icons.add_rounded),
          label: Text(T.s('addSub')),
        ),
      ]),
    );
  }
}

class _StatusText extends StatelessWidget {
  const _StatusText({required this.state});
  final AppState state;

  @override
  Widget build(BuildContext context) {
    final on = state.connected;
    final since = (state.status['since'] as num?)?.toInt() ?? 0;
    final elapsed = since > 0 ? DateTime.now().difference(DateTime.fromMillisecondsSinceEpoch(since * 1000)) : Duration.zero;
    final sub = state.busy
        ? T.s('connecting')
        : on
            ? '${fmtDuration(elapsed)} · ${state.settings.selected == null ? T.s('auto') : (state.activeConfig?.name ?? '')}'
            : T.s('tapToConnect');
    return Column(children: [
      AnimatedSwitcher(
        duration: Motion.normal,
        child: Text(on ? T.s('connected') : T.s('notConnected'), key: ValueKey(on), style: Type.statusBig.copyWith(color: on ? X.accent : X.text)),
      ),
      const SizedBox(height: Space.xs),
      Text(sub, style: Type.secondary.copyWith(fontSize: 14, fontFeatures: const [FontFeature.tabularFigures()])),
    ]);
  }
}

class _UsingNow extends StatelessWidget {
  const _UsingNow({required this.state, required this.up, required this.down});
  final AppState state;
  final double up, down;

  @override
  Widget build(BuildContext context) {
    final c = state.activeConfig;
    final delay = c == null ? null : state.delayOf(c.id);
    final alive = ((state.status['outbounds'] as List?) ?? [])
        .map((o) => o as Map)
        .where((o) => o['alive'] == true && o['id'] != c?.id)
        .map((o) => state.sub?.configs.where((x) => x.id == o['id']).firstOrNull?.label ?? o['id'])
        .take(2)
        .toList();
    return Glass(
      child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
        Row(children: [
          Expanded(
            child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
              Text(T.s('usingNow').toUpperCase(), style: Type.overline),
              const SizedBox(height: 2),
              AnimatedSwitcher(
                duration: Motion.normal,
                child: Text(state.connected ? (c?.label ?? '—') : '—', key: ValueKey(c?.id), style: Type.headline),
              ),
            ]),
          ),
          if (state.connected && delay != null && delay > 0) Text('$delay ms', style: Type.number.copyWith(color: X.accent)),
        ]),
        if (state.connected && alive.isNotEmpty) ...[
          const SizedBox(height: Space.s),
          Text('${T.s('bestHere')} ${T.s('nextIfDrops', {'next': alive.join(', ')})}', style: Type.secondary),
        ],
        const SizedBox(height: Space.m),
        const Divider(),
        const SizedBox(height: Space.s),
        Row(children: [
          Expanded(child: _Stat(label: T.s('down'), value: '${fmtBytes(down)}/s')),
          Expanded(child: _Stat(label: T.s('up'), value: '${fmtBytes(up)}/s')),
        ]),
      ]),
    );
  }
}

class _Stat extends StatelessWidget {
  const _Stat({required this.label, required this.value});
  final String label, value;
  @override
  Widget build(BuildContext context) => Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
        Text(label, style: Type.caption),
        const SizedBox(height: 2),
        Text(value, style: Type.stat),
      ]);
}

class _UsageCard extends StatelessWidget {
  const _UsageCard({required this.sub});
  final Sub sub;

  @override
  Widget build(BuildContext context) {
    final u = sub.usage;
    final days = u.daysLeft;
    final resets = u.resets > 0 ? DateTime.fromMillisecondsSinceEpoch(u.resets * 1000).difference(DateTime.now()).inDays : null;
    final color = u.fraction >= 1 ? X.bad : (u.fraction >= 0.85 ? X.warn : X.accent);
    return Glass(
      child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
        Row(children: [
          Expanded(
            child: Text(
              u.quota > 0 ? T.s('ofQuota', {'used': fmtBytes(u.used), 'quota': fmtBytes(u.quota)}) : T.s('used', {'used': fmtBytes(u.used)}),
              style: Type.bodyStrong,
            ),
          ),
          if (resets != null) Text(T.s('resetsIn', {'n': resets}), style: Type.secondary),
        ]),
        if (u.quota > 0) ...[const SizedBox(height: Space.s), Bar(fraction: u.fraction, color: color)],
        const SizedBox(height: Space.s),
        Text(
          [
            if (u.devices > 0) T.s('devices', {'n': u.devices}),
            days == null ? T.s('neverExpires') : (days < 0 ? T.s('expired') : T.s('expiresIn', {'n': days})),
          ].join(' · '),
          style: Type.secondary.copyWith(color: days != null && days < 3 ? X.warn : X.muted),
        ),
      ]),
    );
  }
}
