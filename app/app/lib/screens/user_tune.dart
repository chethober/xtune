import 'package:material_ui/material_ui.dart';

import '../main.dart';
import '../state/models.dart';
import '../ui/glass.dart';
import '../ui/i18n.dart';

/// A user's tuning, as xtune shows it (`xtune users tune NAME --json`, or `GET SUBSCRIPTION/prefs`).
class TuneView {
  TuneView(this.j);
  final Map<String, dynamic> j;

  Map<String, dynamic> get effective => Map<String, dynamic>.from((j['effective'] as Map?) ?? {});
  Map<String, dynamic> get policy => Map<String, dynamic>.from((j['policy'] as Map?) ?? {});
  Map<String, dynamic> get prefs => Map<String, dynamic>.from((j['prefs'] as Map?) ?? {});
  List<Map<String, dynamic>> get configs => ((j['configs'] as List?) ?? []).map((c) => Map<String, dynamic>.from(c as Map)).toList();
  List<String> get presets => ((j['presets'] as List?) ?? []).cast<String>();
  String get preset => j['preset'] as String? ?? '';

  List<String> list(Map<String, dynamic> m, String k) => ((m[k] as List?) ?? []).cast<String>();
  bool locked(String part) => list(effective, 'lock').contains(part);
  bool get selfTune => effective['selfTune'] == true;
  bool get auto => prefs['auto'] != false;

  /// The ID a user's own choice names for config c: through a relay, RELAY-ID.
  static String idOf(Map<String, dynamic> c) => c['id'] as String;
}

// ---------------------------------------------------------------- Check tab: the user's own configs

/// The user's own configs on their xtune subscription: order, hidden ones, and a personal tune.
class MyConfigsPanel extends StatefulWidget {
  const MyConfigsPanel({super.key});
  @override
  State<MyConfigsPanel> createState() => _MyConfigsPanelState();
}

class _MyConfigsPanelState extends State<MyConfigsPanel> {
  bool open = false;

  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addPostFrameCallback((_) {
      final s = AppScope.of(context);
      if (s.prefs == null || s.prefsSubId != s.sub?.id) s.loadPrefs();
    });
  }

  Future<void> _save(Future<String?> f) async {
    final err = await f;
    if (err != null && mounted) ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text(err)));
  }

  @override
  Widget build(BuildContext context) {
    final s = AppScope.of(context);
    final sub = s.sub;
    if (sub == null || sub.local || s.prefs == null || s.prefsSubId != sub.id) return const SizedBox.shrink();
    final v = TuneView(s.prefs!);
    final pins = v.list(v.prefs, 'pin');
    final hide = v.list(v.prefs, 'hide');
    final configs = v.configs.where((c) => c['allowed'] == true).toList();
    final personal = s.personal;
    final chosen = Map<String, dynamic>.from((personal?['chosen'] as Map?) ?? {});
    final results = ((personal?['results'] as List?) ?? []);
    final measured = results.where((r) => (r as Map)['state'] != 'testing').length;
    return Glass(
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Row(
            children: [
              Expanded(child: Text(T.s('myConfigs'), style: Type.bodyStrong)),
              TextButton(onPressed: () => setState(() => open = !open), child: Text(open ? T.s('hide') : T.s('edit'))),
            ],
          ),
          Text(T.s('myConfigsHint'), style: Type.caption),
          if (v.selfTune) ...[
            const SizedBox(height: Space.m),
            if (s.personalRunning) ...[
              Text(T.s('personalTesting', {'n': measured, 'total': results.length}), style: Type.secondary),
              const SizedBox(height: Space.s),
              Bar(fraction: results.isEmpty ? 0 : measured / results.length),
              const SizedBox(height: Space.s),
              OutlinedButton(onPressed: s.stopPersonalTune, child: Text(T.s('stop'))),
            ] else
              FilledButton.tonalIcon(
                key: const ValueKey('personal-tune'),
                onPressed: s.startPersonalTune,
                icon: const Icon(Icons.tune_rounded),
                label: Text(T.s('personalTune')),
              ),
            if (chosen.isNotEmpty) ...[
              const SizedBox(height: Space.s),
              Text(T.s('personalDone'), style: Type.secondary),
              for (final e in chosen.entries) Text('${e.key}: ${(e.value as String).isEmpty ? T.s('asDeployed') : e.value}', style: Type.mono),
            ],
            if (s.personalMessage != null) ...[const SizedBox(height: Space.s), Text(s.personalMessage!, style: Type.caption.copyWith(color: X.bad))],
          ],
          if (open) ...[
            const SizedBox(height: Space.s),
            SwitchListTile(
              contentPadding: EdgeInsets.zero,
              value: v.auto,
              onChanged: (on) => _save(s.savePrefs(auto: on)),
              title: Text(T.s('autoOrder'), style: Type.body),
              subtitle: Text(T.s('autoOrderHint'), style: Type.caption),
            ),
            if (v.locked('order') || v.locked('hide')) Text(T.s('adminLocked'), style: Type.caption),
            for (final c in configs)
              ListTile(
                contentPadding: EdgeInsets.zero,
                dense: true,
                title: Text(TuneView.idOf(c), style: Type.body),
                subtitle: Text(
                  [
                    c['label'] ?? c['kind'],
                    if ((c['via'] as String? ?? '').isNotEmpty) '→ ${c['via']}',
                    if ((c['knobs'] as String? ?? '').isNotEmpty) c['knobs'],
                  ].join(' · '),
                  style: Type.caption,
                ),
                leading: IconButton(
                  tooltip: T.s('pinFirst'),
                  icon: Icon(
                    pins.contains(c['id']) ? Icons.push_pin_rounded : Icons.push_pin_outlined,
                    color: pins.contains(c['id']) ? X.accent : X.muted,
                  ),
                  onPressed: v.locked('order')
                      ? null
                      : () => _save(s.savePrefs(pin: pins.contains(c['id']) ? (pins..remove(c['id'])) : [...pins, c['id'] as String])),
                ),
                trailing: Switch(
                  key: ValueKey('show-${c['id']}'),
                  value: !hide.contains(c['id']),
                  onChanged: v.locked('hide') ? null : (on) => _save(s.savePrefs(hide: on ? (hide..remove(c['id'])) : [...hide, c['id'] as String])),
                ),
              ),
          ],
        ],
      ),
    );
  }
}

// ---------------------------------------------------------------- Manage tab: a user's tuning

/// An admin's view of one user's tuning on a managed server: which configs, in which order, with
/// which client settings, and what the user may change themselves.
class UserTunePage extends StatefulWidget {
  const UserTunePage({super.key, required this.server, required this.name});
  final ServerEntry server;
  final String name;
  @override
  State<UserTunePage> createState() => _UserTunePageState();
}

class _UserTunePageState extends State<UserTunePage> {
  TuneView? view;
  String? error;
  bool busy = false;

  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addPostFrameCallback((_) => load());
  }

  Future<void> load() async {
    try {
      final r = await AppScope.of(context).manage('manage.userTune', widget.server, {'name': widget.name});
      view = TuneView(Map<String, dynamic>.from(r as Map));
      error = null;
    } catch (e) {
      error = '$e';
    }
    if (mounted) setState(() {});
  }

  Future<void> change(Map<String, dynamic> c) async {
    setState(() => busy = true);
    try {
      final r = await AppScope.of(context).manage('manage.setUserTune', widget.server, {'name': widget.name, 'change': c});
      view = TuneView(Map<String, dynamic>.from(r as Map));
      error = null;
    } catch (e) {
      if (mounted) ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text('$e')));
    }
    if (mounted) setState(() => busy = false);
  }

  Future<void> _knobs(Map<String, dynamic> c) async {
    final v = view!;
    final cur = Map<String, dynamic>.from(((v.policy['knobs'] as Map?) ?? {})[c['id']] as Map? ?? {});
    String fp = cur['fp'] as String? ?? '';
    String frag = cur['frag'] as String? ?? '';
    String mode = cur['mode'] as String? ?? '';
    final ok = await showDialog<bool>(
      context: context,
      builder: (ctx) => StatefulBuilder(
        builder: (ctx, set) => AlertDialog(
          title: Text(T.s('clientSettings', {'id': c['id']})),
          content: Column(
            mainAxisSize: MainAxisSize.min,
            children: [
              _pick(T.s('fingerprint'), fp, ['', 'chrome', 'firefox', 'safari', 'edge', 'randomized', 'ios', 'android'], (x) => set(() => fp = x)),
              _pick(T.s('fragment'), frag, ['', 'none', 'frag-mild', 'frag-aggr'], (x) => set(() => frag = x)),
              if ((c['kind'] as String? ?? '').contains('xhttp'))
                _pick(T.s('xhttpMode'), mode, ['', 'auto', 'packet-up', 'stream-up', 'stream-one'], (x) => set(() => mode = x)),
            ],
          ),
          actions: [
            TextButton(onPressed: () => Navigator.pop(ctx, false), child: Text(T.s('cancel'))),
            FilledButton(onPressed: () => Navigator.pop(ctx, true), child: Text(T.s('save'))),
          ],
        ),
      ),
    );
    if (ok != true) return;
    await change({
      'knobs': {
        c['id']: {'fp': ?(fp.isEmpty ? null : fp), 'frag': ?(frag.isEmpty ? null : frag), 'mode': ?(mode.isEmpty ? null : mode)},
      },
    });
  }

  Widget _pick(String label, String value, List<String> values, void Function(String) on) => DropdownButtonFormField<String>(
    initialValue: values.contains(value) ? value : '',
    decoration: InputDecoration(labelText: label),
    items: [for (final x in values) DropdownMenuItem(value: x, child: Text(x.isEmpty ? T.s('asDeployed') : x))],
    onChanged: (x) => on(x ?? ''),
  );

  @override
  Widget build(BuildContext context) {
    final v = view;
    final children = <Widget>[];
    if (error != null) children.add(Notice(error!));
    if (v != null) {
      final deny = v.list(v.policy, 'deny');
      final pin = v.list(v.policy, 'pin');
      final lock = v.list(v.policy, 'lock');
      children.addAll([
        Glass(
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: [
              SwitchListTile(
                contentPadding: EdgeInsets.zero,
                value: v.policy['selfTune'] == true,
                onChanged: busy ? null : (on) => change({'selfTune': on}),
                title: Text(T.s('selfTune'), style: Type.body),
                subtitle: Text(T.s('selfTuneHint'), style: Type.caption),
              ),
              if (v.presets.isNotEmpty)
                DropdownButtonFormField<String>(
                  initialValue: v.preset,
                  decoration: InputDecoration(labelText: T.s('preset')),
                  items: [
                    DropdownMenuItem(value: '', child: Text(T.s('none'))),
                    for (final p in v.presets) DropdownMenuItem(value: p, child: Text(p)),
                  ],
                  onChanged: busy ? null : (p) => change({'preset': p ?? ''}),
                ),
              const SizedBox(height: Space.m),
              Text(T.s('userMayChange'), style: Type.caption),
              Wrap(
                spacing: Space.s,
                children: [
                  for (final part in ['order', 'hide', 'knobs'])
                    FilterChip(
                      label: Text(T.s('lock_$part')),
                      selected: !lock.contains(part),
                      onSelected: busy
                          ? null
                          : (free) => change({
                              'lock': free ? (lock..remove(part)) : [...lock, part],
                            }),
                    ),
                ],
              ),
            ],
          ),
        ),
        SectionLabel(T.s('userConfigs')),
        for (final c in v.configs)
          ListTile(
            contentPadding: EdgeInsets.zero,
            title: Text(TuneView.idOf(c), style: Type.body),
            subtitle: Text(
              [
                c['label'] ?? c['kind'],
                if ((c['via'] as String? ?? '').isNotEmpty) '→ ${c['via']}',
                if ((c['knobs'] as String? ?? '').isNotEmpty) c['knobs'],
                if ((c['message'] as String? ?? '').isNotEmpty) c['message'],
              ].join(' · '),
              style: Type.caption,
            ),
            leading: IconButton(
              tooltip: T.s('pinFirst'),
              icon: Icon(pin.contains(c['id']) ? Icons.push_pin_rounded : Icons.push_pin_outlined, color: pin.contains(c['id']) ? X.accent : X.muted),
              onPressed: busy
                  ? null
                  : () => change({
                      'pin': pin.contains(c['id']) ? (pin..remove(c['id'])) : [...pin, c['id'] as String],
                    }),
            ),
            trailing: Row(
              mainAxisSize: MainAxisSize.min,
              children: [
                IconButton(
                  tooltip: T.s('clientSettings', {'id': c['id']}),
                  icon: const Icon(Icons.tune_rounded),
                  onPressed: busy ? null : () => _knobs(c),
                ),
                Switch(
                  key: ValueKey('allow-${c['id']}'),
                  value: !deny.contains(c['id']) && c['allowed'] == true,
                  onChanged: busy || (c['allowed'] != true && !deny.contains(c['id']))
                      ? null
                      : (on) => change({
                          'deny': on ? (deny..remove(c['id'])) : [...deny, c['id'] as String],
                        }),
                ),
              ],
            ),
          ),
        const SizedBox(height: Space.m),
        OutlinedButton(onPressed: busy ? null : () => change({'clear': true}), child: Text(T.s('tuneReset'))),
      ]);
    }
    return XPage(
      title: T.s('userTuneTitle', {'name': widget.name}),
      subtitle: T.s('userTuneHint'),
      onRefresh: load,
      children: v == null && error == null ? [const Center(child: CircularProgressIndicator())] : children,
    );
  }
}
