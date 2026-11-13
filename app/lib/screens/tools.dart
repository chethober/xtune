import 'dart:io';

import 'package:file_selector/file_selector.dart';
import 'package:flutter/services.dart';
import 'package:material_ui/material_ui.dart';
import 'package:path_provider/path_provider.dart';
import 'package:share_plus/share_plus.dart';

import '../main.dart';
import '../ui/fmt.dart';
import '../ui/glass.dart';
import '../ui/i18n.dart';

// ---------------------------------------------------------------- leak test

/// Is traffic leaving through the connection, and DNS too?
class LeakTestPage extends StatefulWidget {
  const LeakTestPage({super.key});
  @override
  State<LeakTestPage> createState() => _LeakTestPageState();
}

class _LeakTestPageState extends State<LeakTestPage> {
  Map<String, dynamic>? r;
  String? error;
  bool busy = false;

  Future<void> run() async {
    setState(() {
      busy = true;
      error = null;
      r = null;
    });
    try {
      r = await AppScope.of(context).leakTest();
    } catch (e) {
      error = '$e';
    }
    if (mounted) setState(() => busy = false);
  }

  @override
  Widget build(BuildContext context) {
    final s = AppScope.of(context);
    final res = r;
    return XPage(
      title: T.s('leakTest'),
      subtitle: T.s('leakTestHint'),
      back: true,
      children: [
        if (!s.connected) Notice(T.s('leakConnectFirst'), color: X.warn, icon: Icons.info_outline_rounded),
        if (error != null) Notice(error!),
        if (res != null) ...[
          Glass(
            child: Row(
              children: [
                Icon(
                  res['tunneled'] == true && res['dnsLeak'] != true ? Icons.verified_user_rounded : Icons.gpp_maybe_rounded,
                  color: res['tunneled'] == true && res['dnsLeak'] != true ? X.accent : X.warn,
                  size: 32,
                ),
                const SizedBox(width: Space.m),
                Expanded(child: Text(T.s(_conclusion(res)), style: Type.bodyStrong)),
              ],
            ),
          ),
          SectionLabel(T.s('leakAddress')),
          Group(
            children: [
              _row(T.s('leakExit'), '${res['exitIp'] ?? '?'}  ${_flag(res['exitCountry'] as String?)}'),
              _row(
                T.s('leakDirect'),
                '${(res['directIp'] as String?)?.isNotEmpty == true ? res['directIp'] : '?'}  ${_flag(res['directCountry'] as String?)}',
              ),
              _row(T.s('leakIpv6'), res['ipv6'] == 'tunneled' ? T.s('leakIpv6Tunneled') : T.s('leakIpv6Blocked')),
            ],
          ),
          SectionLabel(T.s('leakDns')),
          if ((res['dnsError'] as String?)?.isNotEmpty == true) Notice(res['dnsError'] as String, color: X.warn, icon: Icons.info_outline_rounded),
          Group(
            children: [
              for (final d in (res['resolvers'] as List? ?? []).cast<Map>())
                _row('${d['ip']}', '${_flag(d['country'] as String?)} ${d['asn'] ?? ''}', mono: true),
            ],
          ),
        ],
        const SizedBox(height: Space.xl),
        FilledButton.icon(
          style: FilledButton.styleFrom(minimumSize: const Size.fromHeight(52)),
          onPressed: busy || !s.connected ? null : run,
          icon: busy ? const SizedBox(width: 18, height: 18, child: CircularProgressIndicator(strokeWidth: 2)) : const Icon(Icons.play_arrow_rounded),
          label: Text(busy ? T.s('leakRunning') : T.s('leakRun')),
        ),
        const SizedBox(height: Space.s),
        Text(T.s('leakPrivacy'), style: Type.caption),
      ],
    );
  }

  static String _conclusion(Map<String, dynamic> r) {
    if (r['tunneled'] != true) return 'leakNotTunneled';
    if (r['dnsLeak'] == true) return 'leakDnsLeak';
    if ((r['dnsError'] as String?)?.isNotEmpty == true) return 'leakDnsUnknown';
    return 'leakNone';
  }

  static String _flag(String? cc) {
    if (cc == null || cc.length != 2) return cc ?? '';
    final up = cc.toUpperCase();
    final flag = String.fromCharCodes([0x1F1E6 + up.codeUnitAt(0) - 65, 0x1F1E6 + up.codeUnitAt(1) - 65]);
    return '$flag $up';
  }

  Widget _row(String k, String v, {bool mono = false}) => ListTile(
    title: Text(k, style: mono ? Type.mono.copyWith(fontSize: 13) : null),
    trailing: Text(v, style: Type.secondary, textDirection: TextDirection.ltr),
  );
}

// ---------------------------------------------------------------- timeline

const _kindIcons = <String, IconData>{
  'connect': Icons.power_rounded,
  'disconnect': Icons.power_off_rounded,
  'switch': Icons.swap_horiz_rounded,
  'network': Icons.wifi_rounded,
  'pause': Icons.pause_circle_outline_rounded,
  'error': Icons.error_outline_rounded,
};

/// When the device connected, switched configs, moved networks or lost the connection, and why.
class TimelinePage extends StatefulWidget {
  const TimelinePage({super.key});
  @override
  State<TimelinePage> createState() => _TimelinePageState();
}

class _TimelinePageState extends State<TimelinePage> {
  List<Map<String, dynamic>> events = [];
  bool loading = true;

  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addPostFrameCallback((_) => load());
  }

  Future<void> load() async {
    try {
      events = await AppScope.of(context).timeline();
    } catch (_) {}
    if (mounted) setState(() => loading = false);
  }

  @override
  Widget build(BuildContext context) {
    final s = AppScope.of(context);
    String? lastDay;
    return XPage(
      title: T.s('timeline'),
      subtitle: T.s('timelineHint'),
      back: true,
      onRefresh: load,
      actions: [
        RoundIconButton(
          icon: Icons.delete_sweep_outlined,
          tooltip: T.s('clear'),
          onPressed: events.isEmpty
              ? null
              : () async {
                  await s.clearTimeline();
                  await load();
                },
        ),
      ],
      children: [
        if (!loading && events.isEmpty) Glass(child: Text(T.s('timelineEmpty'), style: Type.secondary)),
        for (final e in events) ...[
          if (fmtDay(_at(e)) != lastDay) ...[SectionLabel(lastDay = fmtDay(_at(e)))],
          Padding(
            padding: const EdgeInsets.only(bottom: Space.s),
            child: Row(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                SizedBox(
                  width: 52,
                  child: Text(fmtTime(_at(e)), style: Type.caption.copyWith(fontFeatures: const [FontFeature.tabularFigures()])),
                ),
                Icon(
                  _kindIcons[e['kind']] ?? Icons.circle_outlined,
                  size: 18,
                  color: e['kind'] == 'error' ? X.bad : (e['kind'] == 'connect' ? X.accent : X.muted),
                ),
                const SizedBox(width: Space.s),
                // the core writes these in English: the names of configs and networks are in them
                Expanded(
                  child: Text(e['text'] as String? ?? '', style: Type.body.copyWith(fontSize: 14), textDirection: TextDirection.ltr),
                ),
              ],
            ),
          ),
        ],
      ],
    );
  }

  static DateTime _at(Map<String, dynamic> e) => DateTime.fromMillisecondsSinceEpoch(((e['t'] as num?) ?? 0).toInt() * 1000);
}

// ---------------------------------------------------------------- backup

Future<String?> _askPass(BuildContext context, {required bool confirm}) async {
  final a = TextEditingController(), b = TextEditingController();
  String? err;
  return showXSheet<String>(
    context,
    title: T.s(confirm ? 'backupPassNew' : 'backupPass'),
    builder: (ctx, setState) => Column(
      mainAxisSize: MainAxisSize.min,
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Text(T.s(confirm ? 'backupPassHint' : 'backupPassOpenHint'), style: Type.secondary),
        const SizedBox(height: Space.l),
        TextField(
          controller: a,
          autofocus: true,
          obscureText: true,
          decoration: InputDecoration(labelText: T.s('backupPassphrase'), errorText: confirm ? null : err),
        ),
        if (confirm) ...[
          const SizedBox(height: Space.m),
          TextField(
            controller: b,
            obscureText: true,
            decoration: InputDecoration(labelText: T.s('lockPinAgain'), errorText: err),
          ),
        ],
        const SizedBox(height: Space.l),
        Row(
          mainAxisAlignment: MainAxisAlignment.end,
          children: [
            TextButton(onPressed: () => Navigator.pop(ctx), child: Text(T.s('cancel'))),
            const SizedBox(width: Space.s),
            FilledButton(
              onPressed: () {
                if (confirm && a.text.length < 8) {
                  setState(() => err = T.s('backupPassShort'));
                } else if (confirm && a.text != b.text) {
                  setState(() => err = T.s('lockPinMismatch'));
                } else if (a.text.isNotEmpty) {
                  Navigator.pop(ctx, a.text);
                }
              },
              child: Text(T.s('continue')),
            ),
          ],
        ),
      ],
    ),
  );
}

/// Saves everything (subscriptions, settings, servers with their logins) to a sealed file.
Future<void> exportBackup(BuildContext context) async {
  final s = AppScope.of(context);
  final pass = await _askPass(context, confirm: true);
  if (pass == null || !context.mounted) return;
  try {
    final text = await s.exportBackup(pass);
    final name = 'xtune-backup-${fmtDate(DateTime.now()).replaceAll('/', '-')}.xtb';
    if (Platform.isAndroid) {
      final f = File('${(await getTemporaryDirectory()).path}/$name');
      await f.writeAsString(text);
      await SharePlus.instance.share(ShareParams(files: [XFile(f.path)], title: name));
    } else {
      final loc = await getSaveLocation(suggestedName: name);
      if (loc == null) return;
      await File(loc.path).writeAsString(text);
      if (context.mounted) ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text(T.s('backupSaved'))));
    }
  } catch (e) {
    if (context.mounted) ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text('$e')));
  }
}

/// Restores a backup file (or pasted text).
Future<void> importBackup(BuildContext context) async {
  final s = AppScope.of(context);
  String? text;
  final f = await openFile();
  if (f != null) {
    text = await f.readAsString();
  } else {
    final clip = await Clipboard.getData(Clipboard.kTextPlain);
    if (clip?.text?.trim().startsWith('xtune-backup:') == true) text = clip!.text;
  }
  if (text == null || !context.mounted) return;
  final pass = await _askPass(context, confirm: false);
  if (pass == null || !context.mounted) return;
  try {
    final (subs, servers) = await s.importBackup(text, pass);
    if (context.mounted) {
      ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text(T.s('backupRestored', {'subs': subs, 'servers': servers}))));
    }
  } catch (e) {
    if (context.mounted) ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text('$e')));
  }
}
