import 'dart:io';
import 'dart:async';

import 'package:material_ui/material_ui.dart';
import 'package:url_launcher/url_launcher.dart';

import '../main.dart';
import '../state/app_state.dart';
import '../state/models.dart';
import '../ui/glass.dart';
import '../ui/i18n.dart';
import '../ui/power_button.dart';
import '../ui/route_map.dart';
import 'add_sub.dart';
import 'options.dart';
import 'settings.dart' show updateNow;

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

  bool _captive = false;
  int _ticks = 0;

  void _rates() {
    if (!mounted) return;
    if (_ticks++ % 5 == 0 && !AppScope.of(context).connected) {
      AppScope.of(context).captivePortal().then((v) {
        if (mounted && v != _captive) setState(() => _captive = v);
      });
    }
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
      actions: [
        if (sub != null) ...[_SubChip(state: s), if (!sub.local) _RefreshSub(state: s, sub: sub)],
      ],
      // the route and the usage: beside the button on wide screens, under it on phones
      side: [
        if (sub != null) ...[
          const SizedBox(height: Space.m),
          RouteMap(state: s),
          const SizedBox(height: Space.m),
          GestureDetector(
            onTap: () => pushPage(context, AppScope(state: s, child: const UsagePage())),
            child: _UsageCard(sub: sub),
          ),
        ],
      ],
      sideWidth: 480,
      children: [
        if (s.network != null)
          Row(
            mainAxisAlignment: MainAxisAlignment.center,
            children: [
              Icon(Icons.wifi_rounded, size: 14, color: X.muted),
              const SizedBox(width: 6),
              Flexible(
                child: Text('${s.network!['name'] ?? ''} · ${s.network!['asn'] ?? ''}', style: Type.secondary, overflow: TextOverflow.ellipsis),
              ),
            ],
          ),
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
        ],
        if (s.update != null && s.settings.updateMode != 'off') ...[
          const SizedBox(height: Space.m),
          Glass(
            child: Row(
              children: [
                Icon(Icons.system_update_rounded, color: X.accent),
                const SizedBox(width: Space.m),
                Expanded(child: Text(T.s('updateAvailable', {'v': s.update!['latest'] ?? ''}), style: Type.body)),
                TextButton(onPressed: () => updateNow(context), child: Text(T.s('updateShort'))),
              ],
            ),
          ),
        ],
        if (sub != null && _quotaWarning(sub) != null) ...[
          const SizedBox(height: Space.m),
          Notice(_quotaWarning(sub)!, color: X.warn, icon: Icons.data_usage_rounded),
        ],
        if (_captive && !s.connected) ...[
          const SizedBox(height: Space.m),
          Glass(
            child: Row(
              children: [
                Icon(Icons.wifi_password_rounded, color: X.warn),
                const SizedBox(width: Space.m),
                Expanded(child: Text(T.s('captiveHint'), style: Type.secondary)),
                TextButton(
                  onPressed: () => launchUrl(Uri.parse('http://neverssl.com'), mode: LaunchMode.externalApplication),
                  child: Text(T.s('captiveSignIn')),
                ),
              ],
            ),
          ),
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

/// Nearly out of data or time on the subscription.
String? _quotaWarning(Sub sub) {
  final u = sub.usage;
  if (u.quota > 0 && u.fraction >= 0.9) {
    return T.s('quotaNearly', {'left': fmtBytes((u.quota - u.used).clamp(0, u.quota))});
  }
  final d = u.daysLeft;
  if (d != null && d <= 3) return d < 0 ? T.s('subExpired') : T.s('daysLeftWarn', {'n': d});
  return null;
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
        child: Row(
          mainAxisSize: MainAxisSize.min,
          children: [
            ConstrainedBox(
              constraints: const BoxConstraints(maxWidth: 190),
              child: Text(state.sub!.name, overflow: TextOverflow.ellipsis, style: Type.body.copyWith(fontSize: 14)),
            ),
            const SizedBox(width: 6),
            const Icon(Icons.expand_more_rounded, size: 18),
          ],
        ),
      ),
    );
  }
}

/// Refreshes the subscription now, saying how it went.
Future<void> refreshSubNow(BuildContext context, AppState state, Sub sub) async {
  final messenger = ScaffoldMessenger.maybeOf(context);
  try {
    await state.refreshSub(sub);
    messenger?.showSnackBar(SnackBar(content: Text(T.s('subUpdated', {'name': sub.name}))));
  } catch (e) {
    messenger?.showSnackBar(SnackBar(content: Text('$e')));
  }
}

/// Asks, then removes the subscription (disconnecting first when it is the one in use).
Future<bool> removeSubAsk(BuildContext context, AppState state, Sub sub) async {
  final ok = await showDialog<bool>(
    context: context,
    builder: (c) => AlertDialog(
      content: Text(T.s('confirmRemoveSub', {'name': sub.name})),
      actions: [
        TextButton(onPressed: () => Navigator.pop(c, false), child: Text(T.s('cancel'))),
        FilledButton(
          style: FilledButton.styleFrom(backgroundColor: X.bad),
          onPressed: () => Navigator.pop(c, true),
          child: Text(T.s('remove')),
        ),
      ],
    ),
  );
  if (ok != true) return false;
  await state.removeSub(sub);
  return true;
}

/// The header's refresh button: fetches the subscription in use again.
class _RefreshSub extends StatefulWidget {
  const _RefreshSub({required this.state, required this.sub});
  final AppState state;
  final Sub sub;
  @override
  State<_RefreshSub> createState() => _RefreshSubState();
}

class _RefreshSubState extends State<_RefreshSub> {
  bool busy = false;

  @override
  Widget build(BuildContext context) => busy
      ? const Padding(
          padding: EdgeInsetsDirectional.only(start: Space.s),
          child: SizedBox(
            width: 44,
            height: 44,
            child: Padding(padding: EdgeInsets.all(12), child: CircularProgressIndicator(strokeWidth: 2)),
          ),
        )
      : RoundIconButton(
          icon: Icons.refresh_rounded,
          tooltip: T.s('refreshSub'),
          onPressed: () async {
            setState(() => busy = true);
            await refreshSubNow(context, widget.state, widget.sub);
            if (mounted) setState(() => busy = false);
          },
        );
}

void _pickSub(BuildContext context, AppState state) {
  final refreshing = <String>{};
  showXSheet<void>(
    context,
    title: T.s('subscriptions'),
    padded: false,
    builder: (ctx, setSheet) => Column(
      mainAxisSize: MainAxisSize.min,
      children: [
        for (final s in state.subs)
          ListTile(
            contentPadding: const EdgeInsetsDirectional.only(start: Space.l, end: Space.s),
            leading: Icon(
              s.id == state.sub?.id ? Icons.radio_button_checked : Icons.radio_button_off,
              color: s.id == state.sub?.id ? X.accent : X.muted,
            ),
            title: Text(s.name, overflow: TextOverflow.ellipsis),
            subtitle: Text(
              [
                if (s.user.isNotEmpty) s.user,
                T.s('nConfigs', {'n': s.allConfigs.length}),
              ].join(' · '),
            ),
            trailing: Row(
              mainAxisSize: MainAxisSize.min,
              children: [
                if (!s.local)
                  refreshing.contains(s.id)
                      ? const SizedBox(
                          width: 48,
                          child: Center(child: SizedBox(width: 18, height: 18, child: CircularProgressIndicator(strokeWidth: 2))),
                        )
                      : IconButton(
                          tooltip: T.s('refreshSub'),
                          icon: const Icon(Icons.refresh_rounded),
                          onPressed: () async {
                            setSheet(() => refreshing.add(s.id));
                            await refreshSubNow(context, state, s);
                            if (ctx.mounted) setSheet(() => refreshing.remove(s.id));
                          },
                        ),
                IconButton(
                  tooltip: T.s('removeSub'),
                  icon: Icon(Icons.delete_outline_rounded, color: X.bad),
                  onPressed: () async {
                    if (!await removeSubAsk(ctx, state, s) || !ctx.mounted) return;
                    if (state.subs.isEmpty) {
                      Navigator.pop(ctx);
                    } else {
                      setSheet(() {});
                    }
                  },
                ),
              ],
            ),
            onTap: () {
              Navigator.pop(ctx);
              state.selectSub(s);
            },
          ),
        ListTile(
          contentPadding: const EdgeInsetsDirectional.only(start: Space.l, end: Space.s),
          leading: const Icon(Icons.add_rounded),
          title: Text(T.s('addSub')),
          onTap: () {
            Navigator.pop(ctx);
            showAddSub(context);
          },
        ),
      ],
    ),
  );
}

class _EmptyState extends StatelessWidget {
  const _EmptyState({required this.onAdd});
  final VoidCallback onAdd;

  @override
  Widget build(BuildContext context) {
    return Glass(
      padding: const EdgeInsets.all(Space.xxl),
      child: Column(
        children: [
          Icon(Icons.link_rounded, size: 48, color: X.accent),
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
          if (Platform.isAndroid) ...[
            const SizedBox(height: Space.s),
            OutlinedButton.icon(
              style: OutlinedButton.styleFrom(minimumSize: const Size(200, 48)),
              onPressed: () => showAddSub(context, scan: true),
              icon: const Icon(Icons.qr_code_scanner_rounded),
              label: Text(T.s('scanQr')),
            ),
          ],
        ],
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
        : state.pausedUntil > DateTime.now().millisecondsSinceEpoch
        ? T.s('pausedUntil', {'t': TimeOfDay.fromDateTime(DateTime.fromMillisecondsSinceEpoch(state.pausedUntil)).format(context)})
        : T.s('tapToConnect');
    return Column(
      children: [
        AnimatedSwitcher(
          duration: Motion.normal,
          child: Text(
            on ? T.s('connected') : T.s('notConnected'),
            key: ValueKey(on),
            style: Type.statusBig.copyWith(color: on ? X.accent : X.text),
          ),
        ),
        const SizedBox(height: Space.xs),
        Text(sub, style: Type.secondary.copyWith(fontSize: 14, fontFeatures: const [FontFeature.tabularFigures()])),
      ],
    );
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
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              Expanded(
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Text(T.s('usingNow').toUpperCase(), style: Type.overline),
                    const SizedBox(height: 2),
                    AnimatedSwitcher(
                      duration: Motion.normal,
                      child: Text(state.connected ? (c?.label ?? '—') : '—', key: ValueKey(c?.id), style: Type.headline),
                    ),
                  ],
                ),
              ),
              if (state.connected && delay != null && delay > 0) Text('$delay ms', style: Type.number.copyWith(color: X.accent)),
            ],
          ),
          if (state.connected && alive.isNotEmpty) ...[
            const SizedBox(height: Space.s),
            Text('${T.s('bestHere')} ${T.s('nextIfDrops', {'next': alive.join(', ')})}', style: Type.secondary),
          ],
          const SizedBox(height: Space.m),
          const Divider(),
          const SizedBox(height: Space.s),
          Row(
            children: [
              Expanded(
                child: _Stat(label: T.s('down'), value: '${fmtBytes(down)}/s'),
              ),
              Expanded(
                child: _Stat(label: T.s('up'), value: '${fmtBytes(up)}/s'),
              ),
            ],
          ),
        ],
      ),
    );
  }
}

class _Stat extends StatelessWidget {
  const _Stat({required this.label, required this.value});
  final String label, value;
  @override
  Widget build(BuildContext context) => Column(
    crossAxisAlignment: CrossAxisAlignment.start,
    children: [
      Text(label, style: Type.caption),
      const SizedBox(height: 2),
      Text(value, style: Type.stat),
    ],
  );
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
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              Expanded(
                child: Text(
                  u.quota > 0 ? T.s('ofQuota', {'used': fmtBytes(u.used), 'quota': fmtBytes(u.quota)}) : T.s('used', {'used': fmtBytes(u.used)}),
                  style: Type.bodyStrong,
                ),
              ),
              if (resets != null) Text(T.s('resetsIn', {'n': resets}), style: Type.secondary),
            ],
          ),
          if (u.quota > 0) ...[const SizedBox(height: Space.s), Bar(fraction: u.fraction, color: color)],
          const SizedBox(height: Space.s),
          Text(
            [
              if (u.devices > 0) T.s('devices', {'n': u.devices}),
              days == null ? T.s('neverExpires') : (days < 0 ? T.s('expired') : T.s('expiresIn', {'n': days})),
            ].join(' · '),
            style: Type.secondary.copyWith(color: days != null && days < 3 ? X.warn : X.muted),
          ),
        ],
      ),
    );
  }
}
