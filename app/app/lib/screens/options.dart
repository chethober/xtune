import 'dart:io';

import 'package:flutter/services.dart';
import 'package:material_ui/material_ui.dart';
import 'package:path_provider/path_provider.dart';
import 'package:share_plus/share_plus.dart';

import '../main.dart';
import '../state/models.dart';
import '../ui/fmt.dart';
import '../ui/glass.dart';
import '../ui/i18n.dart';

// ---------------------------------------------------------------- routing rules

/// Xray tells apps apart only on Linux and Windows (Android keeps other apps' connections from it).
List<String> get ruleMatches => ['domain', 'full', 'keyword', 'geosite', 'ip', 'geoip', 'port', if (Platform.isLinux || Platform.isWindows) 'app'];
const ruleActions = ['direct', 'proxy', 'via', 'block'];

/// Ready-made rule sets.
final rulePresets = <String, List<RouteRule>>{
  'presetIran': [RouteRule(match: 'geosite', value: 'category-ir', action: 'direct'), RouteRule(match: 'geoip', value: 'ir', action: 'direct')],
  'presetChina': [RouteRule(match: 'geosite', value: 'cn', action: 'direct'), RouteRule(match: 'geoip', value: 'cn', action: 'direct')],
  'presetRussia': [RouteRule(match: 'geosite', value: 'category-ru', action: 'direct'), RouteRule(match: 'geoip', value: 'ru', action: 'direct')],
  'presetTrackers': [
    RouteRule(match: 'geosite', value: 'category-ads-all', action: 'block'),
    RouteRule(match: 'geosite', value: 'win-spy', action: 'block'),
  ],
  'presetStreaming': [
    RouteRule(match: 'geosite', value: 'netflix', action: 'proxy'),
    RouteRule(match: 'geosite', value: 'youtube', action: 'proxy'),
    RouteRule(match: 'geosite', value: 'spotify', action: 'proxy'),
  ],
};

class RulesPage extends StatelessWidget {
  const RulesPage({super.key});

  @override
  Widget build(BuildContext context) {
    final s = AppScope.of(context);
    final rules = s.settings.rules;
    Future<void> save(List<RouteRule> r) async {
      await s.updateSettings((x) => x.rules = r);
      if (s.connected) await s.connect();
    }

    return XPage(
      title: T.s('rules'),
      subtitle: T.s('rulesHint'),
      back: true,
      actions: [
        XMenuButton<String>(
          tooltip: T.s('presets'),
          icon: Icons.auto_awesome_rounded,
          onSelected: (k) => save([...rules, ...rulePresets[k]!.map((r) => RouteRule.fromJson(r.toJson()))]),
          items: [for (final k in rulePresets.keys) XMenuItem(k, T.s(k))],
        ),
        RoundIconButton(
          icon: Icons.add_rounded,
          tooltip: T.s('addRule'),
          filled: true,
          onPressed: () async {
            final r = await editRule(context, null);
            if (r != null) await save([...rules, r]);
          },
        ),
      ],
      children: [
        if (rules.isEmpty)
          Glass(child: Text(T.s('noRules'), style: Type.secondary))
        else
          Glass(
            padding: EdgeInsets.zero,
            child: ReorderableListView(
              shrinkWrap: true,
              physics: const NeverScrollableScrollPhysics(),
              buildDefaultDragHandles: false,
              onReorderItem: (a, b) {
                final l = [...rules];
                final r = l.removeAt(a);
                l.insert(b, r);
                save(l);
              },
              children: [
                for (var i = 0; i < rules.length; i++)
                  ListTile(
                    key: ValueKey('rule-$i-${rules[i].match}-${rules[i].value}'),
                    leading: ReorderableDragStartListener(index: i, child: const Icon(Icons.drag_indicator_rounded)),
                    title: Text(rules[i].value, textDirection: TextDirection.ltr),
                    subtitle: Text(
                      rules[i].action == 'via'
                          ? '${T.s('match_${rules[i].match}')} · ${s.sub?.allConfigs.where((c) => c.id == rules[i].config).firstOrNull?.name ?? T.s('viaGone')}'
                          : T.s('match_${rules[i].match}'),
                    ),
                    trailing: Row(
                      mainAxisSize: MainAxisSize.min,
                      children: [
                        Badge2(T.s('act_${rules[i].action}').toUpperCase(), switch (rules[i].action) {
                          'block' => X.bad,
                          'direct' => X.blue,
                          'via' => X.warn,
                          _ => X.accent,
                        }),
                        IconButton(
                          tooltip: T.s('delete'),
                          icon: const Icon(Icons.close_rounded, size: 20),
                          onPressed: () => save([...rules]..removeAt(i)),
                        ),
                      ],
                    ),
                    onTap: () async {
                      final r = await editRule(context, rules[i]);
                      if (r != null) save([...rules]..[i] = r);
                    },
                  ),
              ],
            ),
          ),
        const SizedBox(height: Space.m),
        Text(T.s('rulesOrder'), style: Type.caption),
      ],
    );
  }
}

Future<RouteRule?> editRule(BuildContext context, RouteRule? r) {
  var match = r?.match ?? 'domain';
  var action = r?.action ?? 'direct';
  var config = r?.config ?? '';
  final ctl = TextEditingController(text: r?.value ?? '');
  // the configs a rule can send traffic through: the ones this app runs itself
  final configs = (AppScope.of(context).sub?.allConfigs ?? []).where((c) => !c.handoff && !c.hidden).toList();
  return showDialog<RouteRule>(
    context: context,
    builder: (ctx) => StatefulBuilder(
      builder: (ctx, setState) => AlertDialog(
        title: Text(r == null ? T.s('addRule') : T.s('editRule')),
        content: SingleChildScrollView(
          child: Column(
            mainAxisSize: MainAxisSize.min,
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: [
              XDropdown<String>(
                label: T.s('ruleMatch'),
                value: match,
                items: [for (final m in ruleMatches) XMenuItem(m, T.s('match_$m'))],
                onChanged: (v) => setState(() => match = v),
              ),
              const SizedBox(height: Space.m),
              TextField(
                controller: ctl,
                autofocus: r == null,
                textDirection: TextDirection.ltr,
                decoration: InputDecoration(labelText: T.s('ruleValue'), hintText: T.s('hint_$match')),
              ),
              const SizedBox(height: Space.m),
              Segmented(
                labels: [for (final a in ruleActions) T.s('act_$a')],
                selected: ruleActions.indexOf(action),
                onSelect: (i) => setState(() => action = ruleActions[i]),
              ),
              if (action == 'via') ...[
                const SizedBox(height: Space.m),
                if (configs.isEmpty)
                  Text(T.s('viaNoConfigs'), style: Type.caption)
                else
                  XDropdown<String>(
                    label: T.s('viaConfig'),
                    helper: T.s('viaHint'),
                    value: configs.any((c) => c.id == config) ? config : configs.first.id,
                    items: [for (final c in configs) XMenuItem(c.id, c.name)],
                    onChanged: (v) => setState(() => config = v),
                  ),
              ],
            ],
          ),
        ),
        actions: [
          TextButton(onPressed: () => Navigator.pop(ctx), child: Text(T.s('cancel'))),
          FilledButton(
            onPressed: () {
              final v = ctl.text.trim();
              if (v.isEmpty) return;
              if (action == 'via') {
                if (configs.isEmpty) return;
                if (!configs.any((c) => c.id == config)) config = configs.first.id;
              }
              Navigator.pop(ctx, RouteRule(match: match, value: v, action: action, config: action == 'via' ? config : ''));
            },
            child: const Text('OK'),
          ),
        ],
      ),
    ),
  );
}

// ---------------------------------------------------------------- tweaks

const fingerprints = ['', 'chrome', 'firefox', 'safari', 'ios', 'android', 'edge', 'randomized'];

/// Edits tweaks (for every config, or one): fragmentation, fingerprint, MTU, Mux. Returns the new
/// tweaks, or null when cancelled.
Future<Map<String, dynamic>?> editTweaks(
  BuildContext context, {
  required String title,
  required Map<String, dynamic> current,
  bool perConfig = false,
}) {
  final t = Map<String, dynamic>.from(current);
  final len = TextEditingController(text: t['fragLength'] as String? ?? '');
  final delay = TextEditingController(text: t['fragDelay'] as String? ?? '');
  final mtu = TextEditingController(text: (t['mtu'] as int?)?.toString() ?? '');
  String tri(String? v) => v ?? '';
  return showXSheet<Map<String, dynamic>>(
    context,
    title: title,
    builder: (ctx, setState) {
      Widget triState(String key, String label, String hint) => Padding(
        padding: const EdgeInsets.only(bottom: Space.m),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text(label, style: Type.bodyStrong),
            Text(hint, style: Type.caption),
            const SizedBox(height: Space.s),
            Segmented(
              labels: [T.s(perConfig ? 'tweakInherit' : 'tweakServer'), T.s('on'), T.s('off')],
              selected: ['', 'on', 'off'].indexOf(tri(t[key] as String?)),
              onSelect: (i) => setState(() => i == 0 ? t.remove(key) : t[key] = ['', 'on', 'off'][i]),
            ),
          ],
        ),
      );
      return Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Text(T.s('tweaksHint'), style: Type.secondary),
          const SizedBox(height: Space.l),
          triState('fragment', T.s('fragment'), T.s('fragmentHint')),
          if (t['fragment'] == 'on')
            Padding(
              padding: const EdgeInsets.only(bottom: Space.m),
              child: Row(
                children: [
                  Expanded(
                    child: TextField(
                      controller: len,
                      textDirection: TextDirection.ltr,
                      decoration: InputDecoration(labelText: T.s('fragLength'), hintText: '100-200'),
                    ),
                  ),
                  const SizedBox(width: Space.s),
                  Expanded(
                    child: TextField(
                      controller: delay,
                      textDirection: TextDirection.ltr,
                      decoration: InputDecoration(labelText: T.s('fragDelay'), hintText: '10-20'),
                    ),
                  ),
                ],
              ),
            ),
          XDropdown<String>(
            label: T.s('fingerprint'),
            helper: T.s('fingerprintHint'),
            value: fingerprints.contains(t['fingerprint']) ? t['fingerprint'] as String? ?? '' : '',
            items: [for (final f in fingerprints) XMenuItem(f, f.isEmpty ? T.s(perConfig ? 'tweakInherit' : 'tweakServer') : f)],
            onChanged: (v) => setState(() => v.isEmpty ? t.remove('fingerprint') : t['fingerprint'] = v),
          ),
          const SizedBox(height: Space.l),
          triState('mux', T.s('mux'), T.s('muxHint')),
          TextField(
            controller: mtu,
            keyboardType: TextInputType.number,
            inputFormatters: [FilteringTextInputFormatter.digitsOnly],
            decoration: InputDecoration(labelText: T.s('mtu'), helperText: T.s('mtuHint'), hintText: '1280'),
          ),
          const SizedBox(height: Space.xl),
          Row(
            children: [
              TextButton(onPressed: () => Navigator.pop(ctx, <String, dynamic>{}), child: Text(T.s('reset'))),
              const Spacer(),
              TextButton(onPressed: () => Navigator.pop(ctx), child: Text(T.s('cancel'))),
              const SizedBox(width: Space.s),
              FilledButton(
                onPressed: () {
                  void set(String k, String v) => v.trim().isEmpty ? t.remove(k) : t[k] = v.trim();
                  set('fragLength', len.text);
                  set('fragDelay', delay.text);
                  final m = int.tryParse(mtu.text);
                  (m == null || m < 576 || m > 1500) ? t.remove('mtu') : t['mtu'] = m;
                  Navigator.pop(ctx, t);
                },
                child: Text(T.s('save')),
              ),
            ],
          ),
        ],
      );
    },
  );
}

/// A one-line summary of tweaks, for a settings row.
String tweaksSummary(Map<String, dynamic> t) {
  final parts = <String>[
    if (t['fragment'] == 'on') T.s('fragment'),
    if (t['fingerprint'] != null) '${t['fingerprint']}',
    if (t['mux'] == 'on') 'Mux',
    if (t['mtu'] != null) 'MTU ${t['mtu']}',
  ];
  return parts.isEmpty ? T.s('tweakNone') : parts.join(' · ');
}

// ---------------------------------------------------------------- lists (bypass domains, trusted Wi-Fi)

class ListEditorPage extends StatefulWidget {
  const ListEditorPage({super.key, required this.title, required this.hint, required this.items, required this.onSave, required this.placeholder});
  final String title, hint, placeholder;
  final List<String> items;
  final Future<void> Function(List<String>) onSave;

  @override
  State<ListEditorPage> createState() => _ListEditorPageState();
}

class _ListEditorPageState extends State<ListEditorPage> {
  late final items = [...widget.items];
  final ctl = TextEditingController();

  void add() {
    final v = ctl.text.trim();
    if (v.isEmpty || items.contains(v)) return;
    setState(() => items.add(v));
    ctl.clear();
    widget.onSave(items);
  }

  @override
  Widget build(BuildContext context) {
    return XPage(
      title: widget.title,
      subtitle: widget.hint,
      back: true,
      children: [
        TextField(
          controller: ctl,
          textDirection: TextDirection.ltr,
          onSubmitted: (_) => add(),
          decoration: InputDecoration(
            hintText: widget.placeholder,
            suffixIcon: IconButton(tooltip: T.s('add'), icon: const Icon(Icons.add_rounded), onPressed: add),
          ),
        ),
        const SizedBox(height: Space.m),
        if (items.isNotEmpty)
          Group(
            children: [
              for (var i = 0; i < items.length; i++)
                ListTile(
                  title: Text(items[i], textDirection: TextDirection.ltr),
                  trailing: IconButton(
                    tooltip: T.s('delete'),
                    icon: const Icon(Icons.close_rounded, size: 20),
                    onPressed: () {
                      setState(() => items.removeAt(i));
                      widget.onSave(items);
                    },
                  ),
                ),
            ],
          ),
      ],
    );
  }
}

// ---------------------------------------------------------------- the log

class LogViewerPage extends StatefulWidget {
  const LogViewerPage({super.key});
  @override
  State<LogViewerPage> createState() => _LogViewerPageState();
}

class _LogViewerPageState extends State<LogViewerPage> {
  String log = '';
  bool busy = true;

  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addPostFrameCallback((_) => load());
  }

  Future<void> load() async {
    setState(() => busy = true);
    try {
      log = await AppScope.of(context).logTail();
    } catch (e) {
      log = '$e';
    }
    if (mounted) setState(() => busy = false);
  }

  @override
  Widget build(BuildContext context) {
    return XPage.fill(
      title: T.s('connectionLog'),
      actions: [
        RoundIconButton(icon: Icons.refresh_rounded, tooltip: T.s('refresh'), onPressed: busy ? null : load),
        RoundIconButton(icon: Icons.ios_share_rounded, tooltip: T.s('shareDiagnostics'), onPressed: () => shareDiagnostics(context)),
      ],
      body: Glass(
        padding: const EdgeInsets.all(Space.m),
        child: busy
            ? const Center(child: CircularProgressIndicator())
            : SingleChildScrollView(
                reverse: true,
                child: Directionality(
                  textDirection: TextDirection.ltr,
                  child: SelectableText(log.isEmpty ? T.s('logEmpty') : log, style: Type.mono.copyWith(fontSize: 11)),
                ),
              ),
      ),
    );
  }
}

/// Builds the diagnostics bundle (secrets removed) and opens the share sheet with it.
Future<void> shareDiagnostics(BuildContext context) async {
  final s = AppScope.of(context);
  try {
    final text = await s.diagnostics();
    final dir = await getTemporaryDirectory();
    final f = File('${dir.path}/xtune-diagnostics-${DateTime.now().millisecondsSinceEpoch}.txt');
    await f.writeAsString(text);
    await SharePlus.instance.share(ShareParams(files: [XFile(f.path)], title: 'xtune diagnostics'));
  } catch (e) {
    if (context.mounted) ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text('$e')));
  }
}

// ---------------------------------------------------------------- traffic history

class UsagePage extends StatefulWidget {
  const UsagePage({super.key});
  @override
  State<UsagePage> createState() => _UsagePageState();
}

class _UsagePageState extends State<UsagePage> {
  int days = 30;
  bool thisSub = false;
  int? hover; // the bar under the finger or pointer

  @override
  Widget build(BuildContext context) {
    final s = AppScope.of(context);
    final subId = thisSub ? s.sub?.id : null;
    final daily = s.dailyTraffic(days, subId: subId);
    final total = daily.fold<int>(0, (a, d) => a + d.$2);
    final today = daily.isEmpty ? 0 : daily.last.$2;
    final byConfig = s.trafficByConfig(days, subId: subId);
    final names = {for (final c in s.sub?.allConfigs ?? <Config>[]) c.id: c.name};
    final maxDay = daily.fold<int>(1, (a, d) => d.$2 > a ? d.$2 : a);
    final shown = hover != null && hover! < daily.length ? daily[hover!] : null;

    return XPage(
      title: T.s('usage'),
      subtitle: T.s('usageHint'),
      back: true,
      children: [
        Segmented(
          labels: [T.s('days7'), T.s('days30'), T.s('days90')],
          selected: [7, 30, 90].indexOf(days),
          onSelect: (i) => setState(() => days = [7, 30, 90][i]),
        ),
        const SizedBox(height: Space.s),
        if (s.subs.length > 1)
          Segmented(labels: [T.s('allSubs'), s.sub?.name ?? ''], selected: thisSub ? 1 : 0, onSelect: (i) => setState(() => thisSub = i == 1)),
        const SizedBox(height: Space.m),
        Row(
          children: [
            Expanded(
              child: _Hero(label: T.s('lastNDays', {'n': days}), value: fmtBytes(total)),
            ),
            const SizedBox(width: Space.s),
            Expanded(
              child: _Hero(label: T.s('today'), value: fmtBytes(today)),
            ),
          ],
        ),
        const SizedBox(height: Space.m),
        Glass(
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              // the readout for the bar under the finger: the date and amount, never colour alone
              SizedBox(
                height: 20,
                child: Text(
                  shown == null ? T.s('perDay') : '${fmtDate(shown.$1)} · ${fmtBytes(shown.$2)}',
                  style: shown == null ? Type.caption : Type.number,
                ),
              ),
              const SizedBox(height: Space.s),
              SizedBox(
                height: 140,
                child: LayoutBuilder(
                  builder: (context, c) {
                    final slot = c.maxWidth / daily.length;
                    int at(double x) => (x / slot).floor().clamp(0, daily.length - 1);
                    return MouseRegion(
                      onHover: (e) => setState(() => hover = at(e.localPosition.dx)),
                      onExit: (_) => setState(() => hover = null),
                      child: GestureDetector(
                        behavior: HitTestBehavior.opaque,
                        onTapDown: (d) => setState(() => hover = at(d.localPosition.dx)),
                        onHorizontalDragUpdate: (d) => setState(() => hover = at(d.localPosition.dx)),
                        onHorizontalDragEnd: (_) => setState(() => hover = null),
                        child: Row(
                          crossAxisAlignment: CrossAxisAlignment.end,
                          children: [
                            for (var i = 0; i < daily.length; i++)
                              SizedBox(
                                width: slot,
                                child: Padding(
                                  padding: const EdgeInsets.symmetric(horizontal: 1), // the 2px gap between bars
                                  child: Align(
                                    alignment: Alignment.bottomCenter,
                                    child: AnimatedContainer(
                                      duration: Motion.normal,
                                      curve: Motion.curve,
                                      height: daily[i].$2 == 0 ? 2 : (136 * daily[i].$2 / maxDay).clamp(3, 136),
                                      decoration: BoxDecoration(
                                        color: daily[i].$2 == 0 ? X.track : X.accent.withValues(alpha: hover == null || hover == i ? 1 : 0.45),
                                        borderRadius: const BorderRadius.vertical(top: Radius.circular(4)),
                                      ),
                                    ),
                                  ),
                                ),
                              ),
                          ],
                        ),
                      ),
                    );
                  },
                ),
              ),
              const SizedBox(height: Space.xs),
              Container(height: 1, color: X.edge),
              const SizedBox(height: Space.xs),
              Row(
                children: [
                  Text(_short(daily.first.$1), style: Type.caption),
                  const Spacer(),
                  Text(T.s('today'), style: Type.caption),
                ],
              ),
            ],
          ),
        ),
        SectionLabel(T.s('byConfig')),
        if (byConfig.isEmpty)
          Glass(child: Text(T.s('noTraffic'), style: Type.secondary))
        else
          Group(
            children: [
              for (final (id, bytes) in byConfig.take(12))
                Padding(
                  padding: const EdgeInsets.symmetric(horizontal: Space.l, vertical: Space.m),
                  child: Column(
                    crossAxisAlignment: CrossAxisAlignment.stretch,
                    children: [
                      Row(
                        children: [
                          Expanded(
                            child: Text(names[id] ?? id, style: Type.body, overflow: TextOverflow.ellipsis),
                          ),
                          Text(fmtBytes(bytes), style: Type.number),
                        ],
                      ),
                      const SizedBox(height: Space.s),
                      Bar(fraction: bytes / byConfig.first.$2, height: 6),
                    ],
                  ),
                ),
            ],
          ),
      ],
    );
  }

  static String _short(DateTime d) => fmtDay(d);
}

class _Hero extends StatelessWidget {
  const _Hero({required this.label, required this.value});
  final String label, value;
  @override
  Widget build(BuildContext context) => Glass(
    padding: const EdgeInsets.all(14),
    child: Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Text(label, style: Type.caption),
        const SizedBox(height: Space.xs),
        Text(value, style: Type.bigNumber),
      ],
    ),
  );
}
