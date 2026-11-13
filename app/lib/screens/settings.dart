import 'dart:io';

import 'package:flutter/services.dart';
import 'package:material_ui/material_ui.dart';
import 'package:url_launcher/url_launcher.dart';

import '../core/core_client.dart';
import '../main.dart';
import '../state/models.dart';
import '../ui/glass.dart';
import '../ui/i18n.dart';

class SettingsScreen extends StatelessWidget {
  const SettingsScreen({super.key});

  @override
  Widget build(BuildContext context) {
    final s = AppScope.of(context);
    final st = s.settings;
    Future<void> reconnect() async {
      if (s.connected) await s.connect();
    }

    return XPage(
      title: T.s('settings'),
      children: [
        Segmented(
          labels: [T.s('modeVpn'), T.s('modeProxy')],
          selected: st.mode == 'vpn' ? 0 : 1,
          onSelect: (i) async {
            await s.updateSettings((x) => x.mode = i == 0 ? 'vpn' : 'proxy');
            await reconnect();
          },
        ),
        Padding(
          padding: const EdgeInsets.fromLTRB(Space.xs, Space.s, Space.xs, 0),
          child: Text(
            st.mode == 'vpn'
                ? (Platform.isAndroid ? T.s('modeVpnHint') : T.s('adminNeeded'))
                : T.s('modeProxyHint', {'socks': st.socksPort, 'http': st.httpPort}),
            style: Type.caption,
          ),
        ),
        SectionLabel(T.s('routing')),
        Group(children: [
          _Switch(T.s('domestic'), T.s('domesticHint'), st.domestic, (v) => s.updateSettings((x) => x.domestic = v).then((_) => reconnect())),
          _Switch(T.s('blockAds'), T.s('blockAdsHint'), st.blockAds, (v) => s.updateSettings((x) => x.blockAds = v).then((_) => reconnect())),
          _Switch(T.s('dnstt'), T.s('dnsttHint'), st.includeDnstt, (v) => s.updateSettings((x) => x.includeDnstt = v).then((_) => reconnect())),
        ]),
        if (Platform.isAndroid) ...[
          SectionLabel(T.s('perApp')),
          Group(children: [
            Padding(
              padding: const EdgeInsets.all(Space.m),
              child: Segmented(
                labels: [T.s('perAppOff'), T.s('perAppBypass'), T.s('perAppOnly')],
                selected: ['off', 'bypass', 'only'].indexOf(st.perAppMode).clamp(0, 2),
                onSelect: (i) => s.updateSettings((x) => x.perAppMode = ['off', 'bypass', 'only'][i]).then((_) => reconnect()),
              ),
            ),
            if (st.perAppMode != 'off')
              _Value(
                st.perAppMode == 'bypass' ? T.s('perAppBypassTitle') : T.s('perAppOnlyTitle'),
                st.perAppMode == 'bypass' ? T.s('perAppBypassHint') : T.s('perAppOnlyHint'),
                T.s('nApps', {'n': st.apps.length}),
                () => _pickApps(context),
              ),
          ]),
        ],
        SectionLabel(T.s('localProxy')),
        Group(children: [
          _Value(T.s('socksPort'), T.s('socksPortHint'), '${st.socksPort}', () => _editPort(context, socks: true)),
          _Value(T.s('httpPort'), T.s('httpPortHint'), '${st.httpPort}', () => _editPort(context, socks: false)),
          _Switch(T.s('shareLan'), T.s('shareLanHint'), st.shareLan, (v) => s.updateSettings((x) => x.shareLan = v).then((_) => reconnect())),
        ]),
        SectionLabel(T.s('app')),
        Group(children: [
          _Value(T.s('dns'), T.s('dnsHint'), Uri.tryParse(st.dns)?.host ?? st.dns, () => _editDns(context)),
          _Value(T.s('language'), 'English, فارسی', T.languages[st.lang] ?? st.lang, () => _pickLang(context)),
        ]),
        SectionLabel(T.s('subscriptions')),
        Group(children: [
          for (final sub in s.subs) _SubRow(sub: sub),
          if (s.subs.isEmpty) Padding(padding: const EdgeInsets.all(Space.l), child: Text('—', style: Type.secondary)),
        ]),
        const SizedBox(height: Space.xl),
        FutureBuilder(
          future: CoreClient.instance.call('version'),
          builder: (_, snap) => Center(child: Text('xtune client ${(snap.data as Map?)?['version'] ?? ''}', style: Type.caption)),
        ),
      ],
    );
  }

  static Future<void> _pickApps(BuildContext context) async {
    final s = AppScope.of(context);
    final apps = await (CoreClient.instance as AndroidCore).installedApps();
    if (!context.mounted) return;
    final chosen = {...s.settings.apps};
    var query = '';
    await showXSheet<void>(
      context,
      title: s.settings.perAppMode == 'bypass' ? T.s('perAppBypassTitle') : T.s('perAppOnlyTitle'),
      padded: false,
      builder: (ctx, setState) {
        final shown = apps
            .where((a) => query.isEmpty || (a['label'] ?? '').toLowerCase().contains(query) || a['package']!.contains(query))
            .toList()
          ..sort((a, b) {
            final ca = chosen.contains(a['package']) ? 0 : 1, cb = chosen.contains(b['package']) ? 0 : 1;
            return ca != cb ? ca - cb : (a['label'] ?? '').toLowerCase().compareTo((b['label'] ?? '').toLowerCase());
          });
        return Column(mainAxisSize: MainAxisSize.min, children: [
          Padding(
            padding: const EdgeInsets.fromLTRB(Space.l, 0, Space.l, Space.s),
            child: TextField(
              onChanged: (v) => setState(() => query = v.trim().toLowerCase()),
              decoration: InputDecoration(prefixIcon: const Icon(Icons.search_rounded), hintText: T.s('search')),
            ),
          ),
          for (final a in shown)
            CheckboxListTile(
              value: chosen.contains(a['package']),
              title: Text(a['label'] ?? a['package']!),
              subtitle: Text(a['package']!, style: Type.caption.copyWith(fontSize: 11)),
              onChanged: (v) => setState(() => v == true ? chosen.add(a['package']!) : chosen.remove(a['package'])),
            ),
        ]);
      },
    );
    await s.updateSettings((x) => x.apps = chosen.toList());
    if (s.connected) await s.connect();
  }

  static Future<void> _editPort(BuildContext context, {required bool socks}) async {
    final s = AppScope.of(context);
    final ctl = TextEditingController(text: '${socks ? s.settings.socksPort : s.settings.httpPort}');
    String? err;
    final v = await showDialog<int>(
      context: context,
      builder: (ctx) => StatefulBuilder(
        builder: (ctx, setState) => AlertDialog(
          title: Text(socks ? T.s('socksPort') : T.s('httpPort')),
          content: TextField(
            controller: ctl,
            autofocus: true,
            keyboardType: TextInputType.number,
            inputFormatters: [FilteringTextInputFormatter.digitsOnly],
            decoration: InputDecoration(hintText: socks ? '10808' : '10809', errorText: err),
          ),
          actions: [
            TextButton(onPressed: () => Navigator.pop(ctx), child: Text(T.s('cancel'))),
            FilledButton(
              onPressed: () {
                final p = int.tryParse(ctl.text) ?? 0;
                final other = socks ? s.settings.httpPort : s.settings.socksPort;
                if (p < 1024 || p > 65535) {
                  setState(() => err = T.s('portRange'));
                } else if (p == other) {
                  setState(() => err = T.s('portSame'));
                } else {
                  Navigator.pop(ctx, p);
                }
              },
              child: const Text('OK'),
            ),
          ],
        ),
      ),
    );
    if (v == null) return;
    await s.updateSettings((x) => socks ? x.socksPort = v : x.httpPort = v);
    if (s.connected) await s.connect();
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
    if (v != null && v.startsWith('https://')) {
      await s.updateSettings((x) => x.dns = v);
      if (s.connected) await s.connect();
    }
  }

  static Future<void> _pickLang(BuildContext context) async {
    final s = AppScope.of(context);
    final v = await showDialog<String>(
      context: context,
      builder: (ctx) => SimpleDialog(
        title: Text(T.s('language')),
        children: [for (final e in T.languages.entries) SimpleDialogOption(onPressed: () => Navigator.pop(ctx, e.key), child: Text(e.value))],
      ),
    );
    if (v != null) await s.updateSettings((x) => x.lang = v);
  }
}

class _Switch extends StatelessWidget {
  const _Switch(this.label, this.hint, this.value, this.onChanged);
  final String label, hint;
  final bool value;
  final ValueChanged<bool> onChanged;

  @override
  Widget build(BuildContext context) => SwitchListTile(value: value, onChanged: onChanged, title: Text(label), subtitle: Text(hint));
}

class _Value extends StatelessWidget {
  const _Value(this.label, this.hint, this.value, this.onTap);
  final String label, hint, value;
  final VoidCallback onTap;

  @override
  Widget build(BuildContext context) => ListTile(
        onTap: onTap,
        title: Text(label),
        subtitle: Text(hint),
        trailing: Row(mainAxisSize: MainAxisSize.min, children: [
          Text(value, style: Type.secondary),
          const SizedBox(width: Space.xs),
          const Icon(Icons.chevron_right_rounded, color: X.muted, size: 20),
        ]),
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
      subtitle: Text('${sub.user} · ${T.s('nConfigs', {'n': sub.configs.length})}'),
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
