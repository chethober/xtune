import 'dart:io';

import 'package:flutter/services.dart';
import 'package:material_ui/material_ui.dart';
import 'package:url_launcher/url_launcher.dart';

import '../core/core_client.dart';
import '../main.dart';
import '../state/models.dart';
import '../ui/glass.dart';
import '../ui/i18n.dart';
import 'options.dart';

class SettingsScreen extends StatelessWidget {
  const SettingsScreen({super.key});

  @override
  Widget build(BuildContext context) {
    final s = AppScope.of(context);
    final st = s.settings;
    final android = Platform.isAndroid;
    Future<void> reconnect() async {
      if (s.connected) await s.connect();
    }

    Future<void> set(void Function(Settings) f, {bool reconnects = true}) async {
      await s.updateSettings(f);
      if (reconnects) await reconnect();
    }

    Future<void> syncAuto() async {
      if (android) {
        await (CoreClient.instance as AndroidCore).native('setAuto', {
          'boot': s.settings.autoConnectBoot,
          'wifi': s.settings.autoConnectWifi,
          'trusted': s.settings.trustedWifi,
        });
      }
    }

    void push(Widget page) => pushPage(context, AppScope(state: s, child: page));

    return XPage(
      title: T.s('settings'),
      children: [
        Segmented(
          labels: [T.s('modeVpn'), T.s('modeProxy')],
          selected: st.mode == 'vpn' ? 0 : 1,
          onSelect: (i) => set((x) => x.mode = i == 0 ? 'vpn' : 'proxy'),
        ),
        Padding(
          padding: const EdgeInsets.fromLTRB(Space.xs, Space.s, Space.xs, 0),
          child: Text(
            st.mode == 'vpn'
                ? (android ? T.s('modeVpnHint') : T.s('adminNeeded'))
                : T.s('modeProxyHint', {'socks': st.socksPort, 'http': st.httpPort}),
            style: Type.caption,
          ),
        ),

        // ------------------------------------------------ appearance
        SectionLabel(T.s('appearance')),
        Group(
          children: [
            Padding(
              padding: const EdgeInsets.all(Space.m),
              child: Segmented(
                labels: [T.s('themeSystem'), T.s('themeDark'), T.s('themeBlack'), T.s('themeLight')],
                selected: ['system', 'dark', 'black', 'light'].indexOf(st.theme).clamp(0, 3),
                onSelect: (i) => set((x) => x.theme = ['system', 'dark', 'black', 'light'][i], reconnects: false),
              ),
            ),
            Padding(
              padding: const EdgeInsets.fromLTRB(Space.l, 0, Space.l, Space.m),
              child: Row(
                children: [
                  Text(T.s('accent'), style: Type.body),
                  const Spacer(),
                  for (final a in Palette.accents.keys)
                    Padding(
                      padding: const EdgeInsetsDirectional.only(start: Space.s),
                      child: Semantics(
                        button: true,
                        selected: st.accent == a,
                        label: T.s('accent_$a'),
                        child: InkWell(
                          customBorder: const CircleBorder(),
                          onTap: () => set((x) => x.accent = a, reconnects: false),
                          child: AnimatedContainer(
                            duration: Motion.fast,
                            width: 36,
                            height: 36,
                            decoration: BoxDecoration(
                              shape: BoxShape.circle,
                              color: X.isLight ? Palette.accents[a]!.$2 : Palette.accents[a]!.$1,
                              border: Border.all(color: st.accent == a ? X.text : Colors.transparent, width: 2.5),
                            ),
                            child: st.accent == a ? Icon(Icons.check_rounded, size: 18, color: X.onAccent) : null,
                          ),
                        ),
                      ),
                    ),
                ],
              ),
            ),
            _Value(T.s('language'), 'English, فارسی', T.languages[st.lang] ?? st.lang, () => _pickLang(context)),
          ],
        ),

        // ------------------------------------------------ connection
        SectionLabel(T.s('connection')),
        Group(
          children: [
            _Switch(T.s('autoLaunch'), T.s('autoLaunchHint'), st.autoConnectLaunch, (v) => set((x) => x.autoConnectLaunch = v, reconnects: false)),
            if (android) ...[
              _Switch(T.s('autoBoot'), T.s('autoBootHint'), st.autoConnectBoot, (v) async {
                await set((x) => x.autoConnectBoot = v, reconnects: false);
                await syncAuto();
              }),
              _Switch(T.s('autoWifi'), T.s('autoWifiHint'), st.autoConnectWifi, (v) async {
                if (v) {
                  final granted = await (CoreClient.instance as AndroidCore).native('askLocation') == true;
                  if (!granted && context.mounted) {
                    ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text(T.s('locationNeeded'))));
                  }
                }
                await set((x) => x.autoConnectWifi = v, reconnects: false);
                await syncAuto();
              }),
              if (st.autoConnectWifi)
                _Value(
                  T.s('trustedWifi'),
                  T.s('trustedWifiHint'),
                  T.s('nNetworks', {'n': st.trustedWifi.length}),
                  () => push(
                    ListEditorPage(
                      title: T.s('trustedWifi'),
                      hint: T.s('trustedWifiHint'),
                      placeholder: T.s('wifiName'),
                      items: st.trustedWifi,
                      onSave: (l) async {
                        await s.updateSettings((x) => x.trustedWifi = l);
                        await syncAuto();
                      },
                    ),
                  ),
                ),
            ],
            _Switch(T.s('smart'), T.s('smartHint'), st.smartReconnect, (v) => set((x) => x.smartReconnect = v)),
            if (android)
              _Value(T.s('alwaysOn'), T.s('alwaysOnHint'), '', () => (CoreClient.instance as AndroidCore).native('vpnSettings'))
            else if (Platform.isLinux)
              _Switch(T.s('killSwitch'), T.s('killSwitchHint'), st.killSwitch, (v) => set((x) => x.killSwitch = v))
            else
              ListTile(title: Text(T.s('killSwitch')), subtitle: Text(T.s('killSwitchWindows'))),
            _Value(T.s('tweaks'), T.s('tweaksAllHint'), tweaksSummary(st.tweaks), () async {
              final t = await editTweaks(context, title: T.s('tweaks'), current: st.tweaks);
              if (t != null) await set((x) => x.tweaks = t);
            }),
          ],
        ),

        // ------------------------------------------------ routing
        SectionLabel(T.s('routing')),
        Group(
          children: [
            _Switch(T.s('domestic'), T.s('domesticHint'), st.domestic, (v) => set((x) => x.domestic = v)),
            _Switch(T.s('blockAds'), T.s('blockAdsHint'), st.blockAds, (v) => set((x) => x.blockAds = v)),
            _Value(T.s('rules'), T.s('rulesHint'), T.s('nRules', {'n': st.rules.length}), () => push(const RulesPage())),
            _Value(
              T.s('bypassDomains'),
              T.s('bypassDomainsHint'),
              T.s('nDomains', {'n': st.bypassDomains.length}),
              () => push(
                ListEditorPage(
                  title: T.s('bypassDomains'),
                  hint: T.s('bypassDomainsHint'),
                  placeholder: 'example.com',
                  items: st.bypassDomains,
                  onSave: (l) => set((x) => x.bypassDomains = l),
                ),
              ),
            ),
            _Switch(T.s('bypassPush'), T.s('bypassPushHint'), st.bypassPush, (v) => set((x) => x.bypassPush = v)),
            _Switch(T.s('bypassLan'), T.s('bypassLanHint'), !st.proxyLan, (v) => set((x) => x.proxyLan = !v)),
            _Switch(T.s('ipv6'), T.s('ipv6Hint'), st.ipv6, (v) => set((x) => x.ipv6 = v)),
            _Switch(T.s('blockQuic'), T.s('blockQuicHint'), st.blockQuic, (v) => set((x) => x.blockQuic = v)),
            _Switch(T.s('dnstt'), T.s('dnsttHint'), st.includeDnstt, (v) => set((x) => x.includeDnstt = v)),
          ],
        ),

        // ------------------------------------------------ DNS
        SectionLabel(T.s('dns')),
        Group(
          children: [
            _Value(T.s('remoteDns'), T.s('dnsHint'), _host(st.dns), () => _editDns(context, domestic: false)),
            _Value(
              T.s('domesticDns'),
              T.s('domesticDnsHint'),
              st.domesticDns.isEmpty ? T.s('same') : _host(st.domesticDns),
              () => _editDns(context, domestic: true),
            ),
            if (st.mode == 'vpn') _Switch(T.s('fakeDns'), T.s('fakeDnsHint'), st.fakeDns, (v) => set((x) => x.fakeDns = v)),
          ],
        ),

        if (android) ...[
          SectionLabel(T.s('perApp')),
          Group(
            children: [
              Padding(
                padding: const EdgeInsets.all(Space.m),
                child: Segmented(
                  labels: [T.s('perAppOff'), T.s('perAppBypass'), T.s('perAppOnly')],
                  selected: ['off', 'bypass', 'only'].indexOf(st.perAppMode).clamp(0, 2),
                  onSelect: (i) => set((x) => x.perAppMode = ['off', 'bypass', 'only'][i]),
                ),
              ),
              if (st.perAppMode != 'off')
                _Value(
                  st.perAppMode == 'bypass' ? T.s('perAppBypassTitle') : T.s('perAppOnlyTitle'),
                  st.perAppMode == 'bypass' ? T.s('perAppBypassHint') : T.s('perAppOnlyHint'),
                  T.s('nApps', {'n': st.apps.length}),
                  () => _pickApps(context),
                ),
            ],
          ),
        ],

        SectionLabel(T.s('localProxy')),
        Group(
          children: [
            _Value(T.s('socksPort'), T.s('socksPortHint'), '${st.socksPort}', () => _editPort(context, socks: true)),
            _Value(T.s('httpPort'), T.s('httpPortHint'), '${st.httpPort}', () => _editPort(context, socks: false)),
            _Switch(T.s('shareLan'), T.s('shareLanHint'), st.shareLan, (v) => set((x) => x.shareLan = v)),
          ],
        ),

        SectionLabel(T.s('subscriptions')),
        Group(
          children: [
            _Value(
              T.s('autoUpdate'),
              T.s('autoUpdateHint'),
              st.autoUpdateHours == 0 ? T.s('never') : T.s('everyNHours', {'n': st.autoUpdateHours}),
              () => _pickUpdate(context),
            ),
            for (final sub in s.subs) _SubRow(sub: sub),
          ],
        ),

        SectionLabel(T.s('diagnostics')),
        Group(
          children: [
            _Value(T.s('usage'), T.s('usageHint'), '', () => push(const UsagePage())),
            _Value(T.s('connectionLog'), T.s('connectionLogHint'), '', () => push(const LogViewerPage())),
            _Value(T.s('shareDiagnostics'), T.s('shareDiagnosticsHint'), '', () => shareDiagnostics(context)),
          ],
        ),
        const SizedBox(height: Space.xl),
        FutureBuilder(
          future: CoreClient.instance.call('version'),
          builder: (_, snap) => Center(child: Text('xtune client ${(snap.data as Map?)?['version'] ?? ''}', style: Type.caption)),
        ),
      ],
    );
  }

  static String _host(String dns) {
    final u = Uri.tryParse(dns);
    if (u != null && u.host.isNotEmpty) return u.host;
    return dns.replaceFirst(RegExp(r'^\w+://'), '');
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
        final shown = apps.where((a) => query.isEmpty || (a['label'] ?? '').toLowerCase().contains(query) || a['package']!.contains(query)).toList()
          ..sort((a, b) {
            final ca = chosen.contains(a['package']) ? 0 : 1, cb = chosen.contains(b['package']) ? 0 : 1;
            return ca != cb ? ca - cb : (a['label'] ?? '').toLowerCase().compareTo((b['label'] ?? '').toLowerCase());
          });
        return Column(
          mainAxisSize: MainAxisSize.min,
          children: [
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
          ],
        );
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

  /// Resolvers: DoH (https://), DNS over TCP (tcp://IP) or plain UDP (udp://IP or an IP).
  static Future<void> _editDns(BuildContext context, {required bool domestic}) async {
    final s = AppScope.of(context);
    final presets = domestic
        ? {'': T.s('same'), 'udp://217.218.155.155': 'TCI (217.218.155.155)', 'udp://8.8.8.8': 'Google (8.8.8.8)', 'system': T.s('systemDns')}
        : {
            'https://1.1.1.1/dns-query': 'Cloudflare DoH',
            'https://dns.google/dns-query': 'Google DoH',
            'https://dns.quad9.net/dns-query': 'Quad9 DoH',
            'tcp://1.1.1.1': 'Cloudflare TCP',
            'udp://8.8.8.8': 'Google UDP',
          };
    final current = domestic ? s.settings.domesticDns : s.settings.dns;
    final ctl = TextEditingController(text: presets.containsKey(current) ? '' : current);
    final v = await showDialog<String>(
      context: context,
      builder: (ctx) => SimpleDialog(
        title: Text(domestic ? T.s('domesticDns') : T.s('remoteDns')),
        children: [
          for (final e in presets.entries)
            ListTile(
              leading: Icon(current == e.key ? Icons.radio_button_checked : Icons.radio_button_off, color: current == e.key ? X.accent : X.muted),
              title: Text(e.value),
              onTap: () => Navigator.pop(ctx, e.key),
            ),
          Padding(
            padding: const EdgeInsets.fromLTRB(Space.xxl, Space.s, Space.xxl, Space.s),
            child: TextField(
              controller: ctl,
              textDirection: TextDirection.ltr,
              decoration: InputDecoration(labelText: T.s('customDns'), hintText: 'https://… · tcp://IP · udp://IP'),
              onSubmitted: (t) => Navigator.pop(ctx, t.trim()),
            ),
          ),
        ],
      ),
    );
    if (v == null) return;
    final ok = (v.isEmpty && domestic) || v == 'system' || RegExp(r'^(https://\S+|tcp://\S+|udp://\S+|[0-9a-fA-F.:]+)$').hasMatch(v);
    if (!ok) {
      if (context.mounted) ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text(T.s('dnsInvalid'))));
      return;
    }
    await s.updateSettings((x) => domestic ? x.domesticDns = v : x.dns = v);
    if (s.connected) await s.connect();
  }

  static Future<void> _pickUpdate(BuildContext context) async {
    final s = AppScope.of(context);
    final v = await showDialog<int>(
      context: context,
      builder: (ctx) => SimpleDialog(
        title: Text(T.s('autoUpdate')),
        children: [
          for (final h in [0, 1, 3, 6, 12, 24])
            SimpleDialogOption(onPressed: () => Navigator.pop(ctx, h), child: Text(h == 0 ? T.s('never') : T.s('everyNHours', {'n': h}))),
        ],
      ),
    );
    if (v != null) await s.updateSettings((x) => x.autoUpdateHours = v);
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
    trailing: Row(
      mainAxisSize: MainAxisSize.min,
      children: [
        ConstrainedBox(
          constraints: const BoxConstraints(maxWidth: 140),
          child: Text(value, style: Type.secondary, overflow: TextOverflow.ellipsis),
        ),
        const SizedBox(width: Space.xs),
        Icon(Icons.chevron_right_rounded, color: X.muted, size: 20),
      ],
    ),
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
      subtitle: Text(
        [
          if (sub.user.isNotEmpty) sub.user,
          T.s('nConfigs', {'n': sub.allConfigs.length}),
          if (sub.local) T.s('pasted'),
        ].join(' · '),
      ),
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
          if (!sub.local) PopupMenuItem(value: 'refresh', child: Text(T.s('refresh'))),
          if (sub.usage.page.isNotEmpty) PopupMenuItem(value: 'page', child: Text(T.s('openPage'))),
          PopupMenuItem(value: 'remove', child: Text(T.s('remove'))),
        ],
      ),
    );
  }
}
