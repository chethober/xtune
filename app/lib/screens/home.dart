import 'dart:async';

import 'package:material_ui/material_ui.dart';

import '../main.dart';
import '../state/app_state.dart';
import '../state/models.dart';
import '../ui/glass.dart';
import '../ui/i18n.dart';
import 'add_sub.dart';

const bottomPad = 112.0;

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
    return ListView(
      padding: const EdgeInsets.fromLTRB(20, 12, 20, bottomPad),
      children: [
        Row(children: [
          const Text('xtune', style: TextStyle(fontSize: 22, fontWeight: FontWeight.w700, letterSpacing: -0.5)),
          const Spacer(),
          if (sub != null) _SubChip(state: s),
        ]),
        if (s.network != null)
          Padding(
            padding: const EdgeInsets.only(top: 8),
            child: Row(mainAxisAlignment: MainAxisAlignment.center, children: [
              const Icon(Icons.wifi_rounded, size: 14, color: X.muted),
              const SizedBox(width: 6),
              Flexible(
                child: Text('${s.network!['name'] ?? ''} · ${s.network!['asn'] ?? ''}',
                    style: const TextStyle(fontSize: 13, color: X.muted), overflow: TextOverflow.ellipsis),
              ),
            ]),
          ),
        if (sub == null) ...[
          const SizedBox(height: 60),
          _EmptyState(onAdd: () => showAddSub(context)),
        ] else ...[
          const SizedBox(height: 24),
          Center(child: _PowerButton(state: s)),
          const SizedBox(height: 18),
          _StatusText(state: s),
          const SizedBox(height: 22),
          Segmented(
            labels: [T.s('auto'), T.s('pick')],
            selected: s.settings.selected == null ? 0 : 1,
            onSelect: (i) => i == 0 ? s.select(null) : widget.onOpenConfigs(),
          ),
          const SizedBox(height: 12),
          _UsingNow(state: s, up: _upRate, down: _downRate),
          const SizedBox(height: 12),
          _UsageCard(sub: sub),
        ],
        if (s.connected && (s.status['error'] as String?)?.isNotEmpty == true) ...[
          const SizedBox(height: 12),
          Text(s.status['error'] as String, style: const TextStyle(fontSize: 12, color: X.warn)),
        ],
        if (s.error != null) ...[
          const SizedBox(height: 12),
          Glass(
            color: X.bad.withValues(alpha: 0.12),
            border: X.bad.withValues(alpha: 0.4),
            child: Row(children: [
              const Icon(Icons.error_outline_rounded, color: X.bad),
              const SizedBox(width: 12),
              Expanded(child: Text(s.error!, style: const TextStyle(fontSize: 13))),
            ]),
          ),
        ],
      ],
    );
  }
}

class _SubChip extends StatelessWidget {
  const _SubChip({required this.state});
  final AppState state;

  @override
  Widget build(BuildContext context) {
    final sub = state.sub!;
    return Glass(
      radius: 22,
      padding: EdgeInsets.zero,
      onTap: () => _pickSub(context, state),
      child: Padding(
        padding: const EdgeInsets.symmetric(horizontal: 14, vertical: 11),
        child: Row(mainAxisSize: MainAxisSize.min, children: [
          ConstrainedBox(
            constraints: const BoxConstraints(maxWidth: 190),
            child: Text(sub.name, overflow: TextOverflow.ellipsis, style: const TextStyle(fontSize: 14, fontWeight: FontWeight.w500)),
          ),
          const SizedBox(width: 6),
          const Icon(Icons.expand_more_rounded, size: 18),
        ]),
      ),
    );
  }
}

void _pickSub(BuildContext context, AppState state) {
  showModalBottomSheet<void>(
    context: context,
    backgroundColor: Colors.transparent,
    builder: (ctx) => Padding(
      padding: const EdgeInsets.all(12),
      child: Glass(
        color: const Color(0xE6151B24),
        padding: const EdgeInsets.symmetric(vertical: 12),
        child: Column(mainAxisSize: MainAxisSize.min, children: [
          for (final s in state.subs)
            ListTile(
              leading: Icon(s.id == state.sub?.id ? Icons.radio_button_checked : Icons.radio_button_off,
                  color: s.id == state.sub?.id ? X.accent : X.muted),
              title: Text(s.name),
              subtitle: Text(s.user, style: const TextStyle(color: X.muted)),
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
      ),
    ),
  );
}

class _EmptyState extends StatelessWidget {
  const _EmptyState({required this.onAdd});
  final VoidCallback onAdd;

  @override
  Widget build(BuildContext context) {
    return Glass(
      padding: const EdgeInsets.all(24),
      child: Column(children: [
        const Icon(Icons.link_rounded, size: 48, color: X.accent),
        const SizedBox(height: 12),
        Text(T.s('noSub'), style: const TextStyle(fontSize: 20, fontWeight: FontWeight.w600)),
        const SizedBox(height: 8),
        Text(T.s('noSubHint'), textAlign: TextAlign.center, style: const TextStyle(color: X.muted)),
        const SizedBox(height: 20),
        FilledButton.icon(
          style: FilledButton.styleFrom(minimumSize: const Size(200, 52), backgroundColor: X.accent, foregroundColor: X.bg),
          onPressed: onAdd,
          icon: const Icon(Icons.add_rounded),
          label: Text(T.s('addSub')),
        ),
      ]),
    );
  }
}

class _PowerButton extends StatelessWidget {
  const _PowerButton({required this.state});
  final AppState state;

  @override
  Widget build(BuildContext context) {
    final on = state.connected;
    return Semantics(
      button: true,
      label: on ? T.s('disconnect') : T.s('connect'),
      child: GestureDetector(
        onTap: state.busy ? null : state.toggle,
        child: AnimatedContainer(
          duration: const Duration(milliseconds: 300),
          width: 208,
          height: 208,
          decoration: BoxDecoration(
            shape: BoxShape.circle,
            gradient: on
                ? RadialGradient(colors: [X.accent.withValues(alpha: 0.25), X.accent.withValues(alpha: 0.05), Colors.transparent], stops: const [0, 0.62, 0.7])
                : null,
            color: on ? null : X.glass,
            border: Border.all(color: on ? X.accent.withValues(alpha: 0.4) : X.edge),
          ),
          alignment: Alignment.center,
          child: AnimatedContainer(
            duration: const Duration(milliseconds: 300),
            width: 150,
            height: 150,
            decoration: BoxDecoration(
              shape: BoxShape.circle,
              color: on ? X.accent : X.track,
              boxShadow: on ? [BoxShadow(color: X.accent.withValues(alpha: 0.35), blurRadius: 40)] : null,
            ),
            child: state.busy
                ? const Center(child: SizedBox(width: 44, height: 44, child: CircularProgressIndicator(strokeWidth: 3)))
                : Icon(Icons.power_settings_new_rounded, size: 64, color: on ? X.bg : X.muted),
          ),
        ),
      ),
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
      Text(on ? T.s('connected') : T.s('notConnected'),
          style: TextStyle(fontSize: 26, fontWeight: FontWeight.w600, color: on ? X.accent : X.text)),
      const SizedBox(height: 4),
      Text(sub, style: const TextStyle(fontSize: 14, color: X.muted, fontFeatures: [FontFeature.tabularFigures()])),
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
              Text(T.s('usingNow').toUpperCase(), style: const TextStyle(fontSize: 12, color: X.muted, letterSpacing: 0.6)),
              const SizedBox(height: 2),
              Text(state.connected ? (c?.label ?? '—') : '—', style: const TextStyle(fontSize: 16, fontWeight: FontWeight.w600)),
            ]),
          ),
          if (state.connected && delay != null && delay > 0)
            Text('$delay ms', style: const TextStyle(fontSize: 14, color: X.accent, fontWeight: FontWeight.w600)),
        ]),
        if (state.connected && alive.isNotEmpty) ...[
          const SizedBox(height: 10),
          Text('${T.s('bestHere')} ${T.s('nextIfDrops', {'next': alive.join(', ')})}',
              style: const TextStyle(fontSize: 13, color: X.muted, height: 1.4)),
        ],
        const SizedBox(height: 12),
        const Divider(height: 1, color: X.edge),
        const SizedBox(height: 10),
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
        Text(label, style: const TextStyle(fontSize: 12, color: X.muted)),
        const SizedBox(height: 2),
        Text(value, style: const TextStyle(fontSize: 18, fontWeight: FontWeight.w600, fontFeatures: [FontFeature.tabularFigures()])),
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
                style: const TextStyle(fontWeight: FontWeight.w600)),
          ),
          if (resets != null) Text(T.s('resetsIn', {'n': resets}), style: const TextStyle(color: X.muted, fontSize: 13)),
        ]),
        if (u.quota > 0) ...[const SizedBox(height: 10), Bar(fraction: u.fraction, color: color)],
        const SizedBox(height: 10),
        Text(
          [
            if (u.devices > 0) T.s('devices', {'n': u.devices}),
            days == null ? T.s('neverExpires') : (days < 0 ? T.s('expired') : T.s('expiresIn', {'n': days})),
          ].join(' · '),
          style: TextStyle(fontSize: 13, color: days != null && days < 3 ? X.warn : X.muted),
        ),
      ]),
    );
  }
}
