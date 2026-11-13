import 'dart:io';

import 'package:material_ui/material_ui.dart';
import 'package:url_launcher/url_launcher.dart';

import '../core/core_client.dart';
import '../main.dart';
import '../state/models.dart';
import '../ui/glass.dart';
import '../ui/i18n.dart';
import 'home.dart' show bottomPad;

class SettingsScreen extends StatelessWidget {
  const SettingsScreen({super.key});

  @override
  Widget build(BuildContext context) {
    final s = AppScope.of(context);
    final st = s.settings;
    return ListView(
      padding: const EdgeInsets.fromLTRB(20, 0, 20, bottomPad),
      children: [
        Padding(padding: const EdgeInsets.only(left: 0), child: ScreenTitle(T.s('settings'))),
        Segmented(
          labels: [T.s('modeVpn'), T.s('modeProxy')],
          selected: st.mode == 'vpn' ? 0 : 1,
          onSelect: (i) async {
            await s.updateSettings((x) => x.mode = i == 0 ? 'vpn' : 'proxy');
            if (s.connected) await s.connect();
          },
        ),
        if (st.mode == 'vpn' && !Platform.isAndroid)
          Padding(
            padding: const EdgeInsets.fromLTRB(4, 8, 4, 0),
            child: Text(T.s('adminNeeded'), style: const TextStyle(fontSize: 12, color: X.muted)),
          ),
        if (st.mode == 'proxy')
          const Padding(
            padding: EdgeInsets.fromLTRB(4, 8, 4, 0),
            child: Text('SOCKS 127.0.0.1:10808 · HTTP 127.0.0.1:10809', style: TextStyle(fontSize: 12, color: X.muted)),
          ),
        SectionLabel(T.s('routing')),
        _Group(children: [
          _Switch(T.s('domestic'), T.s('domesticHint'), st.domestic, (v) => s.updateSettings((x) => x.domestic = v)),
          _Switch(T.s('blockAds'), T.s('blockAdsHint'), st.blockAds, (v) => s.updateSettings((x) => x.blockAds = v)),
          _Switch(T.s('dnstt'), T.s('dnsttHint'), st.includeDnstt, (v) => s.updateSettings((x) => x.includeDnstt = v)),
          if (st.mode == 'proxy') _Switch(T.s('shareLan'), T.s('shareLanHint'), st.shareLan, (v) => s.updateSettings((x) => x.shareLan = v)),
          if (Platform.isAndroid)
            _Value(T.s('perApp'), T.s('perAppHint', {'n': st.excludedApps.length}), '', () => _pickApps(context)),
        ]),
        SectionLabel(T.s('app')),
        _Group(children: [
          _Value(T.s('dns'), 'DNS over HTTPS, through the tunnel', Uri.tryParse(st.dns)?.host ?? st.dns, () => _editDns(context)),
          _Value(T.s('language'), 'English, فارسی', T.languages[st.lang] ?? st.lang, () => _pickLang(context)),
        ]),
        SectionLabel(T.s('subscriptions')),
        _Group(children: [
          for (final sub in s.subs) _SubRow(sub: sub),
          if (s.subs.isEmpty) const Padding(padding: EdgeInsets.all(16), child: Text('—', style: TextStyle(color: X.muted))),
        ]),
        const SizedBox(height: 20),
        FutureBuilder(
          future: CoreClient.instance.call('version'),
          builder: (_, snap) => Center(
            child: Text('xtune client ${(snap.data as Map?)?['version'] ?? ''}', style: const TextStyle(fontSize: 12, color: X.muted)),
          ),
        ),
      ],
    );
  }

  static Future<void> _pickApps(BuildContext context) async {
    final s = AppScope.of(context);
    final apps = await (CoreClient.instance as AndroidCore).installedApps();
    if (!context.mounted) return;
    final excluded = {...s.settings.excludedApps};
    await showModalBottomSheet<void>(
      context: context,
      isScrollControlled: true,
      backgroundColor: const Color(0xF0151B24),
      builder: (ctx) => StatefulBuilder(
        builder: (ctx, setState) => DraggableScrollableSheet(
          expand: false,
          initialChildSize: 0.8,
          builder: (ctx, scroll) => ListView(controller: scroll, children: [
            Padding(padding: const EdgeInsets.all(16), child: Text(T.s('perApp'), style: const TextStyle(fontSize: 18, fontWeight: FontWeight.w600))),
            for (final a in apps)
              CheckboxListTile(
                value: excluded.contains(a['package']),
                title: Text(a['label'] ?? a['package']!),
                subtitle: Text(a['package']!, style: const TextStyle(fontSize: 11, color: X.muted)),
                onChanged: (v) => setState(() => v == true ? excluded.add(a['package']!) : excluded.remove(a['package'])),
              ),
          ]),
        ),
      ),
    );
    await s.updateSettings((x) => x.excludedApps = excluded.toList());
  }

  static Future<void> _editDns(BuildContext context) async {
    final s = AppScope.of(context);
    final ctl = TextEditingController(text: s.settings.dns);
    final v = await showDialog<String>(
      context: context,
      builder: (ctx) => AlertDialog(
        title: Text(T.s('dns')),
        content: TextField(controller: ctl, textDirection: TextDirection.ltr, decoration: const InputDecoration(hintText: 'https://1.1.1.1/dns-query')),
        actions: [
          TextButton(onPressed: () => Navigator.pop(ctx), child: Text(T.s('cancel'))),
          FilledButton(onPressed: () => Navigator.pop(ctx, ctl.text.trim()), child: const Text('OK')),
        ],
      ),
    );
    if (v != null && v.startsWith('https://')) await s.updateSettings((x) => x.dns = v);
  }

  static Future<void> _pickLang(BuildContext context) async {
    final s = AppScope.of(context);
    final v = await showDialog<String>(
      context: context,
      builder: (ctx) => SimpleDialog(
        title: Text(T.s('language')),
        children: [
          for (final e in T.languages.entries) SimpleDialogOption(onPressed: () => Navigator.pop(ctx, e.key), child: Text(e.value)),
        ],
      ),
    );
    if (v != null) await s.updateSettings((x) => x.lang = v);
  }
}

class _Group extends StatelessWidget {
  const _Group({required this.children});
  final List<Widget> children;

  @override
  Widget build(BuildContext context) {
    return Glass(
      padding: EdgeInsets.zero,
      child: Column(children: [
        for (var i = 0; i < children.length; i++) ...[
          if (i > 0) const Divider(height: 1, color: X.edge),
          children[i],
        ],
      ]),
    );
  }
}

class _Switch extends StatelessWidget {
  const _Switch(this.label, this.hint, this.value, this.onChanged);
  final String label, hint;
  final bool value;
  final ValueChanged<bool> onChanged;

  @override
  Widget build(BuildContext context) => SwitchListTile(
        value: value,
        onChanged: onChanged,
        activeTrackColor: X.accent,
        title: Text(label, style: const TextStyle(fontSize: 15)),
        subtitle: Text(hint, style: const TextStyle(fontSize: 12, color: X.muted)),
      );
}

class _Value extends StatelessWidget {
  const _Value(this.label, this.hint, this.value, this.onTap);
  final String label, hint, value;
  final VoidCallback onTap;

  @override
  Widget build(BuildContext context) => ListTile(
        onTap: onTap,
        title: Text(label, style: const TextStyle(fontSize: 15)),
        subtitle: Text(hint, style: const TextStyle(fontSize: 12, color: X.muted)),
        trailing: Text(value, style: const TextStyle(color: X.muted)),
      );
}

class _SubRow extends StatelessWidget {
  const _SubRow({required this.sub});
  final Sub sub;

  @override
  Widget build(BuildContext context) {
    final s = AppScope.of(context);
    return ListTile(
      title: Text(sub.name),
      subtitle: Text('${sub.user} · ${sub.configs.length} configs', style: const TextStyle(fontSize: 12, color: X.muted)),
      trailing: PopupMenuButton<String>(
        onSelected: (v) async {
          switch (v) {
            case 'refresh':
              try {
                await s.refreshSub(sub);
              } catch (e) {
                if (context.mounted) ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text('$e')));
              }
            case 'page':
              if (sub.usage.page.isNotEmpty) await launchUrl(Uri.parse(sub.usage.page), mode: LaunchMode.externalApplication);
            case 'remove':
              await s.removeSub(sub);
          }
        },
        itemBuilder: (_) => [
          PopupMenuItem(value: 'refresh', child: Text(T.s('refresh'))),
          if (sub.usage.page.isNotEmpty) PopupMenuItem(value: 'page', child: Text(T.s('openPage'))),
          PopupMenuItem(value: 'remove', child: Text(T.s('remove'))),
        ],
      ),
    );
  }
}
