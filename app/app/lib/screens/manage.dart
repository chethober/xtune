import 'package:flutter/services.dart';
import 'package:material_ui/material_ui.dart';
import 'package:qr_flutter/qr_flutter.dart';

import 'dart:async';

import 'package:share_plus/share_plus.dart';
import 'package:url_launcher/url_launcher.dart';

import '../main.dart';
import '../state/app_state.dart';
import '../state/models.dart';
import '../ui/charts.dart';
import '../ui/glass.dart';
import '../ui/i18n.dart';
import 'cdn.dart';
import 'tune.dart';

class ManageScreen extends StatefulWidget {
  const ManageScreen({super.key});
  @override
  State<ManageScreen> createState() => _ManageScreenState();
}

class _ManageScreenState extends State<ManageScreen> {
  Map<String, dynamic>? overview;
  String? error;
  bool loading = false;
  String? loadedFor;

  Future<void> load(AppState s) async {
    final srv = s.server;
    if (srv == null) return;
    setState(() {
      loading = true;
      error = null;
      loadedFor = srv.id;
    });
    try {
      final o = Map<String, dynamic>.from(await s.manage('manage.overview', srv) as Map);
      if (mounted) setState(() => overview = o);
      unawaited(s.serverWidget(srv, o)); // the home-screen widget shows what this saw
    } catch (e) {
      if (mounted) setState(() => error = e.toString());
    } finally {
      if (mounted) setState(() => loading = false);
    }
  }

  @override
  Widget build(BuildContext context) {
    final s = AppScope.of(context);
    final srv = s.server;
    if (srv != null && loadedFor != srv.id && !loading) {
      overview = null;
      WidgetsBinding.instance.addPostFrameCallback((_) => load(s));
    }
    if (srv == null) {
      return XPage(
        title: T.s('manage'),
        children: [
          const SizedBox(height: Space.xxl),
          Glass(
            padding: const EdgeInsets.all(Space.xxl),
            child: Column(
              children: [
                Icon(Icons.dns_outlined, size: 48, color: X.accent),
                const SizedBox(height: Space.m),
                Text(T.s('noServer'), style: Type.title),
                const SizedBox(height: Space.s),
                Text(T.s('noServerHint'), textAlign: TextAlign.center, style: Type.secondary),
                const SizedBox(height: Space.xl),
                FilledButton.icon(
                  style: FilledButton.styleFrom(minimumSize: const Size(200, 52)),
                  onPressed: () => addServerDialog(context),
                  icon: const Icon(Icons.add_rounded),
                  label: Text(T.s('addServer')),
                ),
              ],
            ),
          ),
        ],
      );
    }
    final o = overview;
    return XPage(
      title: T.s('manage'),
      onRefresh: () => load(s),
      leading: Align(
        alignment: AlignmentDirectional.centerStart,
        child: Glass(
          radius: Radii.card,
          padding: const EdgeInsets.symmetric(horizontal: 14, vertical: 10),
          onTap: () => _pickServer(context, s),
          child: Row(
            mainAxisSize: MainAxisSize.min,
            children: [
              if ((o?['country'] as String?)?.isNotEmpty == true) ...[
                Badge2((o!['country'] as String).toUpperCase(), X.accent),
                const SizedBox(width: Space.s),
              ],
              ConstrainedBox(
                constraints: const BoxConstraints(maxWidth: 180),
                child: Text(srv.name.isNotEmpty ? srv.name : srv.host, overflow: TextOverflow.ellipsis, style: Type.bodyStrong),
              ),
              const SizedBox(width: 6),
              const Icon(Icons.expand_more_rounded, size: 18),
            ],
          ),
        ),
      ),
      actions: [RoundIconButton(icon: Icons.refresh_rounded, tooltip: T.s('refresh'), onPressed: loading ? null : () => load(s))],
      // wide: the users beside the dashboard
      sideWidth: 440,
      sidePane: o == null ? null : UsersPage(key: ValueKey(srv.id), server: srv, embedded: true),
      children: [
        if (loading && o == null)
          const Padding(
            padding: EdgeInsets.all(40),
            child: Center(child: CircularProgressIndicator()),
          ),
        if (error != null) Notice(error!),
        if (o != null) ..._dashboard(context, s, srv, o),
      ],
    );
  }

  List<Widget> _dashboard(BuildContext context, AppState s, ServerEntry srv, Map<String, dynamic> o) {
    final entries = ((o['entries'] as List?) ?? []).map((e) => Map<String, dynamic>.from(e as Map)).toList();
    final networks = ((o['networks'] as List?) ?? []).map((e) => Map<String, dynamic>.from(e as Map)).toList();
    final blockedSomewhere = <String, List<String>>{};
    for (final n in networks) {
      for (final id in (n['blocked'] as List? ?? [])) {
        blockedSomewhere
            .putIfAbsent(id as String, () => [])
            .add((n['name'] as String?)?.isNotEmpty == true ? n['name'] as String : n['asn'] as String);
      }
    }
    return [
      Row(
        children: [
          Dot(X.accent),
          const SizedBox(width: Space.s),
          Expanded(
            child: Text(
              '${T.s('healthy')} · ${o['domain']?.toString().isNotEmpty == true ? o['domain'] : o['host']} · xtune ${o['version']}',
              style: Type.secondary,
              overflow: TextOverflow.ellipsis,
            ),
          ),
        ],
      ),
      const SizedBox(height: Space.m),
      Row(
        children: [
          Expanded(child: _Tile(T.s('online'), '${o['online']}', T.s('ofUsers', {'n': o['users']}))),
          const SizedBox(width: Space.s),
          Expanded(
            child: _Tile(
              T.s('trafficToday'),
              fmtBytes((o['todayBytes'] as num?) ?? 0),
              T.s('thisMonth', {'v': fmtBytes((o['monthBytes'] as num?) ?? 0)}),
            ),
          ),
        ],
      ),
      const SizedBox(height: Space.m),
      Glass(
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Row(
              children: [
                Expanded(child: Text(T.s('configs'), style: Type.bodyStrong)),
                Text(T.s('configsLive', {'n': entries.length}), style: Type.caption),
              ],
            ),
            const SizedBox(height: Space.s),
            for (final e in entries)
              Padding(
                padding: const EdgeInsets.symmetric(vertical: 5),
                child: Row(
                  children: [
                    Dot(blockedSomewhere.containsKey(e['id']) ? X.warn : X.accent),
                    const SizedBox(width: 10),
                    Expanded(child: Text('${e['id']}', style: Type.body.copyWith(fontSize: 14))),
                    Text(
                      blockedSomewhere.containsKey(e['id']) ? 'blocked on ${blockedSomewhere[e['id']]!.join(', ')}' : '${e['kind']} · ${e['port']}',
                      style: Type.caption.copyWith(color: blockedSomewhere.containsKey(e['id']) ? X.warn : X.muted),
                    ),
                  ],
                ),
              ),
          ],
        ),
      ),
      if (networks.isNotEmpty) ...[
        SectionLabel(T.s('networks')),
        Group(
          children: [
            for (final n in networks)
              ListTile(
                title: Text((n['name'] as String?)?.isNotEmpty == true ? n['name'] as String : 'AS${n['asn']}'),
                subtitle: Text('best: ${(n['best'] as List? ?? []).take(3).join(', ')}'),
                trailing: Text('AS${(n['asn'] as String).replaceFirst('AS', '')}', style: Type.caption),
              ),
          ],
        ),
      ],
      const SizedBox(height: Space.m),
      // three across on a phone; on wider pages tiles keep their size and more fit in a row
      GridView.extent(
        maxCrossAxisExtent: 150,
        shrinkWrap: true,
        physics: const NeverScrollableScrollPhysics(),
        mainAxisSpacing: Space.s,
        crossAxisSpacing: Space.s,
        childAspectRatio: 1.35,
        children: [
          _Action(Icons.auto_fix_high_rounded, T.s('tune'), () => _push(context, TunePage(server: srv, overview: o)), accent: true),
          _Action(Icons.system_update_alt_rounded, T.s('update'), () => _update(context, s, srv, o)),
          _Action(Icons.cloud_outlined, T.s('cdnShort'), () => _push(context, CdnWizardPage(server: srv, overview: o))),
          _Action(Icons.group_outlined, T.s('users'), () => _push(context, UsersPage(server: srv))),
          _Action(
            Icons.monitor_heart_outlined,
            T.s('doctor'),
            () => _push(context, ConsolePage(server: srv, title: T.s('doctor'), initial: const ['doctor'], runNow: true)),
          ),
          _Action(Icons.notes_rounded, T.s('logs'), () => _push(context, LogsPage(server: srv))),
          _Action(Icons.terminal_rounded, T.s('console'), () => _push(context, ConsolePage(server: srv, title: T.s('console')))),
        ],
      ),
    ];
  }

  /// Updates xtune and Xray on the server.
  Future<void> _update(BuildContext context, AppState s, ServerEntry srv, Map<String, dynamic> o) async {
    String output = '';
    String? busy;
    var xray = '';
    var asked = false;
    await showXSheet<void>(
      context,
      title: T.s('updateServer'),
      builder: (ctx, setState) {
        Future<void> run(String what) async {
          setState(() {
            busy = what;
            output = '';
          });
          try {
            final r = await s.manage('manage.update', srv, {'what': what});
            output = ((r as Map)['output'] as String? ?? '').trim();
            if (output.isEmpty) output = T.s('done');
          } catch (e) {
            output = '$e';
          }
          if (ctx.mounted) setState(() => busy = null);
        }

        if (!asked) {
          asked = true;
          s
              .manage('manage.xtune', srv, {
                'args': ['xray', 'status'],
              })
              .then((r) {
                if (ctx.mounted) setState(() => xray = ((r as Map)['output'] as String? ?? '').trim());
              })
              .catchError((_) {});
        }
        Widget row(String what, IconData icon, String title, String sub) => ListTile(
          contentPadding: EdgeInsets.zero,
          leading: Icon(icon),
          title: Text(title),
          subtitle: Text(sub, maxLines: 3, overflow: TextOverflow.ellipsis),
          trailing: FilledButton.tonal(
            onPressed: busy != null ? null : () => run(what),
            child: busy == what ? const SizedBox(width: 16, height: 16, child: CircularProgressIndicator(strokeWidth: 2)) : Text(T.s('update')),
          ),
        );
        return Column(
          mainAxisSize: MainAxisSize.min,
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            row('xtune', Icons.memory_rounded, 'xtune', T.s('updateXtune', {'v': o['version'] ?? '?'})),
            row('xray', Icons.bolt_rounded, 'Xray', xray.isEmpty ? T.s('updateXray') : xray),
            if (output.isNotEmpty) ...[
              const SizedBox(height: Space.s),
              ConstrainedBox(
                constraints: const BoxConstraints(maxHeight: 220),
                child: _Output(text: output, busy: false),
              ),
            ],
            const SizedBox(height: Space.s),
            Text(T.s('updateHint'), style: Type.caption),
          ],
        );
      },
    );
    if (mounted) load(s);
  }

  void _push(BuildContext context, Widget page) => pushPage(context, AppScope(state: AppScope.of(context), child: page));

  void _pickServer(BuildContext context, AppState s) {
    showXSheet<void>(
      context,
      title: T.s('servers'),
      padded: false,
      builder: (ctx, _) => Column(
        mainAxisSize: MainAxisSize.min,
        children: [
          for (final x in s.servers)
            ListTile(
              leading: Icon(
                x.id == s.server?.id ? Icons.radio_button_checked : Icons.radio_button_off,
                color: x.id == s.server?.id ? X.accent : X.muted,
              ),
              title: Text(x.name.isNotEmpty ? x.name : x.host),
              subtitle: Text('${x.user}@${x.host}:${x.port}'),
              onTap: () {
                Navigator.pop(ctx);
                s.selectServer(x);
              },
              trailing: IconButton(
                tooltip: T.s('remove'),
                icon: const Icon(Icons.delete_outline_rounded),
                onPressed: () {
                  Navigator.pop(ctx);
                  s.removeServer(x);
                },
              ),
            ),
          ListTile(
            leading: const Icon(Icons.add_rounded),
            title: Text(T.s('addServer')),
            onTap: () {
              Navigator.pop(ctx);
              addServerDialog(context);
            },
          ),
        ],
      ),
    );
  }
}

class _Tile extends StatelessWidget {
  const _Tile(this.label, this.value, this.sub);
  final String label, value, sub;
  @override
  Widget build(BuildContext context) => Glass(
    padding: const EdgeInsets.all(14),
    child: Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Text(label, style: Type.caption),
        const SizedBox(height: Space.xs),
        Text(value, style: Type.bigNumber),
        Text(sub, style: Type.caption),
      ],
    ),
  );
}

class _Action extends StatelessWidget {
  const _Action(this.icon, this.label, this.onTap, {this.accent = false});
  final IconData icon;
  final String label;
  final VoidCallback onTap;
  final bool accent;
  @override
  Widget build(BuildContext context) => Glass(
    radius: Radii.l,
    padding: const EdgeInsets.symmetric(vertical: Space.m, horizontal: Space.xs),
    onTap: onTap,
    child: Column(
      mainAxisAlignment: MainAxisAlignment.center,
      children: [
        Icon(icon, size: 22, color: accent ? X.accent : null),
        const SizedBox(height: 6),
        Text(
          label,
          style: Type.caption.copyWith(color: accent ? X.accent : X.text, fontWeight: accent ? FontWeight.w600 : null),
          maxLines: 1,
          overflow: TextOverflow.ellipsis,
        ),
      ],
    ),
  );
}

Future<void> addServerDialog(BuildContext context) async {
  final s = AppScope.of(context);
  final name = TextEditingController(), host = TextEditingController(), port = TextEditingController(text: '22');
  final user = TextEditingController(text: 'root'), pass = TextEditingController(), key = TextEditingController();
  String? err;
  var busy = false;
  await showXSheet<void>(
    context,
    title: T.s('addServer'),
    builder: (ctx, setState) => Column(
      mainAxisSize: MainAxisSize.min,
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        TextField(
          controller: name,
          decoration: InputDecoration(labelText: T.s('name'), hintText: 'Frankfurt'),
        ),
        const SizedBox(height: 10),
        Row(
          children: [
            Expanded(
              flex: 3,
              child: TextField(
                controller: host,
                textDirection: TextDirection.ltr,
                keyboardType: TextInputType.url,
                decoration: InputDecoration(labelText: T.s('host')),
              ),
            ),
            const SizedBox(width: 10),
            Expanded(
              child: TextField(
                controller: port,
                keyboardType: TextInputType.number,
                decoration: InputDecoration(labelText: T.s('port')),
              ),
            ),
          ],
        ),
        const SizedBox(height: 10),
        TextField(
          controller: user,
          textDirection: TextDirection.ltr,
          decoration: InputDecoration(labelText: T.s('user')),
        ),
        const SizedBox(height: 10),
        TextField(
          controller: pass,
          obscureText: true,
          decoration: InputDecoration(labelText: T.s('password')),
        ),
        const SizedBox(height: 10),
        TextField(
          controller: key,
          minLines: 2,
          maxLines: 4,
          textDirection: TextDirection.ltr,
          style: Type.mono,
          decoration: InputDecoration(labelText: T.s('privateKey'), hintText: '-----BEGIN OPENSSH PRIVATE KEY-----'),
        ),
        if (err != null) ...[const SizedBox(height: 10), Text(err!, style: Type.secondary.copyWith(color: X.bad))],
        const SizedBox(height: 16),
        Row(
          mainAxisAlignment: MainAxisAlignment.end,
          children: [
            TextButton(onPressed: () => Navigator.pop(ctx), child: Text(T.s('cancel'))),
            const SizedBox(width: 8),
            FilledButton(
              style: FilledButton.styleFrom(minimumSize: const Size(96, 48), backgroundColor: X.accent, foregroundColor: X.bg),
              onPressed: busy
                  ? null
                  : () async {
                      setState(() {
                        busy = true;
                        err = null;
                      });
                      try {
                        final e = ServerEntry(
                          id: AppState.newId(),
                          name: name.text.trim(),
                          host: host.text.trim(),
                          port: int.tryParse(port.text.trim()) ?? 22,
                          user: user.text.trim().isEmpty ? 'root' : user.text.trim(),
                        );
                        final r = await s.addServer(e, password: pass.text, key: key.text.trim());
                        if (ctx.mounted) Navigator.pop(ctx);
                        if ((r['version'] as String?)?.isEmpty ?? true) {
                          if (context.mounted) {
                            ScaffoldMessenger.of(context).showSnackBar(
                              const SnackBar(content: Text('xtune is not installed on this server: deploy it from xtune on a computer first')),
                            );
                          }
                        }
                      } catch (e) {
                        setState(() => err = e.toString());
                      } finally {
                        if (ctx.mounted) setState(() => busy = false);
                      }
                    },
              child: busy ? const SizedBox(width: 18, height: 18, child: CircularProgressIndicator(strokeWidth: 2)) : Text(T.s('add')),
            ),
          ],
        ),
      ],
    ),
  );
}

// ---------------------------------------------------------------- users

class UsersPage extends StatefulWidget {
  const UsersPage({super.key, required this.server, this.embedded = false});
  final ServerEntry server;

  /// Beside the dashboard on a wide screen: no back button.
  final bool embedded;
  @override
  State<UsersPage> createState() => _UsersPageState();
}

class _UsersPageState extends State<UsersPage> {
  List<Map<String, dynamic>> users = [];
  bool loading = true;
  String? error;
  String query = '';
  int filter = 0;

  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addPostFrameCallback((_) => load());
  }

  Future<void> load() async {
    setState(() {
      loading = true;
      error = null;
    });
    try {
      final r = await AppScope.of(context).manage('manage.users', widget.server);
      users = ((r as List?) ?? []).map((u) => Map<String, dynamic>.from(u as Map)).toList();
    } catch (e) {
      error = e.toString();
    }
    if (mounted) setState(() => loading = false);
  }

  Future<dynamic> act(String action, String name, [Map<String, dynamic> extra = const {}, bool reload = true]) async {
    try {
      final r = await AppScope.of(context).manage('manage.user', widget.server, {'action': action, 'name': name, ...extra});
      if (reload) await load();
      return r;
    } catch (e) {
      if (mounted) ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text('$e')));
      return null;
    }
  }

  bool _keep(Map<String, dynamic> u) {
    if (query.isNotEmpty && !(u['name'] as String).toLowerCase().contains(query.toLowerCase())) return false;
    final online = (u['online'] as List?)?.isNotEmpty ?? false;
    final exp = (u['expires'] as num?)?.toInt() ?? 0;
    final days = exp > 0 ? DateTime.fromMillisecondsSinceEpoch(exp * 1000).difference(DateTime.now()).inDays : 9999;
    final quota = (u['quota'] as num?) ?? 0;
    final used = ((u['up'] as num?) ?? 0) + ((u['down'] as num?) ?? 0);
    return switch (filter) {
      1 => online,
      2 => days >= 0 && days <= 7,
      3 => quota > 0 && used >= quota * 0.9,
      4 => u['status'] != 'active',
      _ => true,
    };
  }

  @override
  Widget build(BuildContext context) {
    final shown = users.where(_keep).toList();
    return XPage(
      title: T.s('users'),
      back: !widget.embedded,
      onRefresh: load,
      actions: [
        XMenuButton<String>(
          tooltip: T.s('addUser'),
          icon: Icons.person_add_alt_1_rounded,
          items: [
            XMenuItem('one', T.s('addUser'), icon: Icons.person_add_alt_1_rounded),
            XMenuItem('many', T.s('addUsers'), icon: Icons.group_add_rounded),
          ],
          onSelected: (v) => v == 'one' ? _addUser(context) : _addUsers(context),
        ),
      ],
      children: [
        TextField(
          onChanged: (v) => setState(() => query = v),
          decoration: InputDecoration(prefixIcon: const Icon(Icons.search_rounded), hintText: '${T.s('search')} (${users.length})'),
        ),
        const SizedBox(height: Space.m),
        Pills(
          labels: [T.s('all'), T.s('usersOnline'), T.s('usersExpiring'), T.s('usersOverQuota'), T.s('usersOff')],
          selected: filter,
          onSelect: (i) => setState(() => filter = i),
        ),
        const SizedBox(height: Space.m),
        if (loading && users.isEmpty)
          const Padding(
            padding: EdgeInsets.all(40),
            child: Center(child: CircularProgressIndicator()),
          ),
        if (error != null) Notice(error!),
        for (final u in shown)
          Padding(
            padding: const EdgeInsets.only(bottom: Space.s),
            child: _userCard(context, u),
          ),
      ],
    );
  }

  Widget _userCard(BuildContext context, Map<String, dynamic> u) {
    final used = ((u['up'] as num?) ?? 0) + ((u['down'] as num?) ?? 0);
    final quota = (u['quota'] as num?) ?? 0;
    final frac = quota > 0 ? used / quota : 0.0;
    final online = (u['online'] as List?)?.length ?? 0;
    final exp = (u['expires'] as num?)?.toInt() ?? 0;
    final days = exp > 0 ? DateTime.fromMillisecondsSinceEpoch(exp * 1000).difference(DateTime.now()).inDays : null;
    final status = u['status'] as String? ?? '';
    final color = frac >= 1 ? X.bad : (frac >= 0.85 ? X.warn : X.accent);
    return Glass(
      radius: 18,
      padding: const EdgeInsets.all(14),
      onTap: () => _userSheet(context, u),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              Container(
                width: 8,
                height: 8,
                decoration: BoxDecoration(color: online > 0 ? X.accent : X.track, shape: BoxShape.circle),
              ),
              const SizedBox(width: 10),
              Expanded(child: Text(u['name'] as String, style: Type.bodyStrong)),
              Text(
                status != 'active' ? status : (days == null ? '' : '$days days left'),
                style: Type.caption.copyWith(color: status != 'active' ? X.warn : X.muted),
              ),
            ],
          ),
          if (quota > 0) ...[const SizedBox(height: 8), Bar(fraction: frac.toDouble(), color: color, height: 6)],
          const SizedBox(height: 8),
          Row(
            children: [
              Expanded(child: Text(quota > 0 ? '${fmtBytes(used)} of ${fmtBytes(quota)}' : '${fmtBytes(used)} used', style: Type.caption)),
              Text('$online online${(u['devices'] as num? ?? 0) > 0 ? ' of ${u['devices']}' : ''}', style: Type.caption),
            ],
          ),
        ],
      ),
    );
  }

  Future<void> _addUser(BuildContext context) async {
    final name = TextEditingController(), quota = TextEditingController(text: '50'), days = TextEditingController(text: '30');
    final devices = TextEditingController(text: '2');
    var monthly = true;
    final ok = await showDialog<bool>(
      context: context,
      builder: (ctx) => StatefulBuilder(
        builder: (ctx, setState) => AlertDialog(
          title: Text(T.s('addUser')),
          content: SingleChildScrollView(
            child: Column(
              mainAxisSize: MainAxisSize.min,
              children: [
                TextField(
                  controller: name,
                  autofocus: true,
                  textDirection: TextDirection.ltr,
                  decoration: InputDecoration(labelText: T.s('name')),
                ),
                const SizedBox(height: 10),
                TextField(
                  controller: quota,
                  keyboardType: TextInputType.number,
                  decoration: InputDecoration(labelText: T.s('quotaGb')),
                ),
                const SizedBox(height: 10),
                TextField(
                  controller: days,
                  keyboardType: TextInputType.number,
                  decoration: InputDecoration(labelText: T.s('days')),
                ),
                const SizedBox(height: 10),
                TextField(
                  controller: devices,
                  keyboardType: TextInputType.number,
                  decoration: InputDecoration(labelText: T.s('devicesLimit')),
                ),
                SwitchListTile(
                  contentPadding: EdgeInsets.zero,
                  value: monthly,
                  onChanged: (v) => setState(() => monthly = v),
                  title: const Text('Reset usage monthly'),
                ),
              ],
            ),
          ),
          actions: [
            TextButton(onPressed: () => Navigator.pop(ctx, false), child: Text(T.s('cancel'))),
            FilledButton(onPressed: () => Navigator.pop(ctx, true), child: Text(T.s('add'))),
          ],
        ),
      ),
    );
    if (ok != true || name.text.trim().isEmpty) return;
    final r = await act('add', name.text.trim(), {
      'quotaGb': double.tryParse(quota.text) ?? 0,
      'days': int.tryParse(days.text) ?? 0,
      'devices': int.tryParse(devices.text) ?? 0,
      'reset': monthly ? 'monthly' : '',
    });
    if (r != null && context.mounted) _showLinks(context, Map<String, dynamic>.from(r as Map));
  }

  /// Adds several users at once (prefix-01, prefix-02, …) with the same limits, then offers all
  /// their links to share.
  Future<void> _addUsers(BuildContext context) async {
    final prefix = TextEditingController(text: 'user'), count = TextEditingController(text: '5');
    final quota = TextEditingController(text: '50'), days = TextEditingController(text: '30'), devices = TextEditingController(text: '2');
    var monthly = true;
    final ok = await showDialog<bool>(
      context: context,
      builder: (ctx) => StatefulBuilder(
        builder: (ctx, setState) => AlertDialog(
          title: Text(T.s('addUsers')),
          content: SingleChildScrollView(
            child: Column(
              mainAxisSize: MainAxisSize.min,
              children: [
                Row(
                  children: [
                    Expanded(
                      flex: 2,
                      child: TextField(
                        controller: prefix,
                        autofocus: true,
                        textDirection: TextDirection.ltr,
                        decoration: InputDecoration(labelText: T.s('usersPrefix')),
                      ),
                    ),
                    const SizedBox(width: 10),
                    Expanded(
                      child: TextField(
                        controller: count,
                        keyboardType: TextInputType.number,
                        inputFormatters: [FilteringTextInputFormatter.digitsOnly],
                        decoration: InputDecoration(labelText: T.s('usersCount')),
                      ),
                    ),
                  ],
                ),
                const SizedBox(height: 10),
                TextField(
                  controller: quota,
                  keyboardType: TextInputType.number,
                  decoration: InputDecoration(labelText: T.s('quotaGb')),
                ),
                const SizedBox(height: 10),
                TextField(
                  controller: days,
                  keyboardType: TextInputType.number,
                  decoration: InputDecoration(labelText: T.s('days')),
                ),
                const SizedBox(height: 10),
                TextField(
                  controller: devices,
                  keyboardType: TextInputType.number,
                  decoration: InputDecoration(labelText: T.s('devicesLimit')),
                ),
                SwitchListTile(
                  contentPadding: EdgeInsets.zero,
                  value: monthly,
                  onChanged: (v) => setState(() => monthly = v),
                  title: Text(T.s('resetMonthly')),
                ),
              ],
            ),
          ),
          actions: [
            TextButton(onPressed: () => Navigator.pop(ctx, false), child: Text(T.s('cancel'))),
            FilledButton(onPressed: () => Navigator.pop(ctx, true), child: Text(T.s('add'))),
          ],
        ),
      ),
    );
    final n = (int.tryParse(count.text) ?? 0).clamp(0, 100);
    final p = prefix.text.trim();
    if (ok != true || n == 0 || p.isEmpty || !context.mounted) return;
    final taken = users.map((u) => u['name'] as String).toSet();
    final width = '$n'.length < 2 ? 2 : '$n'.length;
    final made = <(String, String)>[];
    var i = 0;
    while (made.length < n && i < n + taken.length + 1) {
      i++;
      final name = '$p-${'$i'.padLeft(width, '0')}';
      if (taken.contains(name)) continue;
      final r = await act('add', name, {
        'quotaGb': double.tryParse(quota.text) ?? 0,
        'days': int.tryParse(days.text) ?? 0,
        'devices': int.tryParse(devices.text) ?? 0,
        'reset': monthly ? 'monthly' : '',
      }, false);
      if (r == null) break; // the error is shown; stop there
      final m = Map<String, dynamic>.from(r as Map);
      made.add((name, (m['subscription'] as String?) ?? ((m['links'] as List?)?.firstOrNull as String? ?? '')));
    }
    await load();
    if (made.isEmpty || !context.mounted) return;
    final text = made.map((x) => '${x.$1}: ${x.$2}').join('\n');
    showXSheet<void>(
      context,
      title: T.s('usersAdded', {'n': made.length}),
      builder: (ctx, _) => Column(
        mainAxisSize: MainAxisSize.min,
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          ConstrainedBox(
            constraints: const BoxConstraints(maxHeight: 260),
            child: SingleChildScrollView(
              child: Directionality(
                textDirection: TextDirection.ltr,
                child: SelectableText(text, style: Type.mono),
              ),
            ),
          ),
          const SizedBox(height: Space.m),
          Row(
            children: [
              Expanded(
                child: OutlinedButton.icon(
                  onPressed: () => Clipboard.setData(ClipboardData(text: text)),
                  icon: const Icon(Icons.copy_rounded),
                  label: Text(T.s('copy')),
                ),
              ),
              const SizedBox(width: 8),
              Expanded(
                child: FilledButton.icon(
                  onPressed: () => SharePlus.instance.share(ShareParams(text: text)),
                  icon: const Icon(Icons.ios_share_rounded),
                  label: Text(T.s('share')),
                ),
              ),
            ],
          ),
        ],
      ),
    );
  }

  void _userSheet(BuildContext context, Map<String, dynamic> u) {
    final name = u['name'] as String;
    final enabled = u['enabled'] == true;
    final online = ((u['online'] as List?) ?? []).cast<String>();
    final blocked = ((u['blocked'] as List?) ?? []).cast<String>();
    final daily = u['daily'] is Map ? Map<String, dynamic>.from(u['daily'] as Map) : null;
    showXSheet<void>(
      context,
      title: name,
      padded: false,
      builder: (ctx, _) => Column(
        mainAxisSize: MainAxisSize.min,
        children: [
          if (daily != null)
            Padding(
              padding: const EdgeInsets.fromLTRB(Space.l, 0, Space.l, Space.m),
              child: DailyBars(days: lastDays(daily, 30), height: 90),
            ),
          if (online.isNotEmpty || blocked.isNotEmpty) ...[
            Padding(
              padding: const EdgeInsetsDirectional.fromSTEB(Space.l, Space.xs, Space.l, Space.xs),
              child: Align(
                alignment: AlignmentDirectional.centerStart,
                child: Text(T.s('devicesNow'), style: Type.caption),
              ),
            ),
            for (final ip in online)
              ListTile(
                dense: true,
                leading: Dot(X.accent),
                title: Text(ip, style: Type.mono.copyWith(fontSize: 13), textDirection: TextDirection.ltr),
                trailing: TextButton(
                  onPressed: () async {
                    Navigator.pop(ctx);
                    final r = await act('kick', name, {'ip': ip, 'minutes': 60});
                    if (r != null && context.mounted) {
                      ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text(T.s('kicked', {'ip': ip}))));
                    }
                  },
                  child: Text(T.s('kick')),
                ),
              ),
            for (final ip in blocked)
              ListTile(
                dense: true,
                leading: Dot(X.bad),
                title: Text(ip, style: Type.mono.copyWith(fontSize: 13), textDirection: TextDirection.ltr),
                subtitle: Text(T.s('blockedNow')),
                trailing: TextButton(
                  onPressed: () {
                    Navigator.pop(ctx);
                    act('unblock', name, {'ip': ip});
                  },
                  child: Text(T.s('unblock')),
                ),
              ),
            const Divider(),
          ],
          ListTile(
            leading: const Icon(Icons.qr_code_2_rounded),
            title: Text(T.s('links')),
            onTap: () async {
              Navigator.pop(ctx);
              final r = await act('links', name);
              if (r != null && context.mounted) _showLinks(context, Map<String, dynamic>.from(r as Map));
            },
          ),
          ListTile(
            leading: const Icon(Icons.update_rounded),
            title: Text(T.s('renew')),
            onTap: () {
              Navigator.pop(ctx);
              act('renew', name);
            },
          ),
          ListTile(
            leading: Icon(enabled ? Icons.pause_circle_outline : Icons.play_circle_outline),
            title: Text(enabled ? T.s('disable') : T.s('enable')),
            onTap: () {
              Navigator.pop(ctx);
              act(enabled ? 'disable' : 'enable', name);
            },
          ),
          ListTile(
            leading: const Icon(Icons.restart_alt_rounded),
            title: Text(T.s('resetUsage')),
            onTap: () {
              Navigator.pop(ctx);
              act('reset-usage', name);
            },
          ),
          ListTile(
            leading: const Icon(Icons.key_rounded),
            title: Text(T.s('rotate')),
            onTap: () {
              Navigator.pop(ctx);
              act('rotate', name);
            },
          ),
          ListTile(
            leading: Icon(Icons.delete_outline_rounded, color: X.bad),
            title: Text(T.s('delete'), style: Type.body.copyWith(color: X.bad)),
            onTap: () async {
              Navigator.pop(ctx);
              final ok = await showDialog<bool>(
                context: context,
                builder: (c) => AlertDialog(
                  content: Text(T.s('confirmDelete', {'name': name})),
                  actions: [
                    TextButton(onPressed: () => Navigator.pop(c, false), child: Text(T.s('cancel'))),
                    FilledButton(
                      style: FilledButton.styleFrom(backgroundColor: X.bad),
                      onPressed: () => Navigator.pop(c, true),
                      child: Text(T.s('delete')),
                    ),
                  ],
                ),
              );
              if (ok == true) act('del', name);
            },
          ),
        ],
      ),
    );
  }

  void _showLinks(BuildContext context, Map<String, dynamic> r) {
    final sub = r['subscription'] as String?;
    final links = ((r['links'] as List?) ?? []).cast<String>();
    final main = sub ?? (links.isNotEmpty ? links.first : '');
    showXSheet<void>(
      context,
      title: '${r['name']}',
      builder: (ctx, _) => Column(
        mainAxisSize: MainAxisSize.min,
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Text(sub != null ? T.s('subLinkHint') : T.s('nLinks', {'n': links.length}), style: Type.secondary),
          if (main.isNotEmpty) ...[
            const SizedBox(height: 16),
            Center(
              child: Container(
                padding: const EdgeInsets.all(Space.s),
                decoration: BoxDecoration(color: Colors.white, borderRadius: BorderRadius.circular(Radii.m)),
                child: QrImageView(data: main, size: 220),
              ),
            ),
            const SizedBox(height: 12),
            SelectableText(main, textDirection: TextDirection.ltr, style: Type.caption),
            const SizedBox(height: 12),
            Row(
              children: [
                Expanded(
                  child: OutlinedButton.icon(
                    onPressed: () => Clipboard.setData(ClipboardData(text: main)),
                    icon: const Icon(Icons.copy_rounded),
                    label: Text(T.s('copy')),
                  ),
                ),
                const SizedBox(width: 8),
                Expanded(
                  child: OutlinedButton.icon(
                    onPressed: () => SharePlus.instance.share(ShareParams(text: main)),
                    icon: const Icon(Icons.share_rounded),
                    label: Text(T.s('share')),
                  ),
                ),
              ],
            ),
            const SizedBox(height: 8),
            // straight to a Telegram chat, the way most people hand links over
            FilledButton.icon(
              onPressed: () =>
                  launchUrl(Uri.https('t.me', '/share/url', {'url': main, 'text': '${r['name']}'}), mode: LaunchMode.externalApplication),
              icon: const Icon(Icons.send_rounded),
              label: Text(T.s('sendTelegram')),
            ),
          ],
        ],
      ),
    );
  }
}

// ---------------------------------------------------------------- console and logs

class ConsolePage extends StatefulWidget {
  const ConsolePage({super.key, required this.server, required this.title, this.initial = const [], this.runNow = false});
  final ServerEntry server;
  final String title;
  final List<String> initial;
  final bool runNow;
  @override
  State<ConsolePage> createState() => _ConsolePageState();
}

class _ConsolePageState extends State<ConsolePage> {
  late final ctl = TextEditingController(text: widget.initial.join(' '));
  String output = '';
  bool busy = false;

  @override
  void initState() {
    super.initState();
    if (widget.runNow) WidgetsBinding.instance.addPostFrameCallback((_) => run());
  }

  Future<void> run() async {
    final args = ctl.text.trim().split(RegExp(r'\s+')).where((a) => a.isNotEmpty).toList();
    if (args.isNotEmpty && args.first == 'xtune') args.removeAt(0);
    if (args.isEmpty) return;
    setState(() {
      busy = true;
      output = '';
    });
    try {
      final r = await AppScope.of(context).manage('manage.xtune', widget.server, {'args': args});
      output = (r as Map)['output'] as String? ?? '';
    } catch (e) {
      output = '$e';
    }
    if (mounted) setState(() => busy = false);
  }

  @override
  Widget build(BuildContext context) {
    return XPage.fill(
      title: widget.title,
      body: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Directionality(
            textDirection: TextDirection.ltr,
            child: TextField(
              controller: ctl,
              onSubmitted: (_) => run(),
              style: Type.mono.copyWith(fontSize: 14),
              decoration: InputDecoration(
                prefixText: 'xtune ',
                hintText: 'users list',
                suffixIcon: IconButton(tooltip: T.s('run'), onPressed: busy ? null : run, icon: const Icon(Icons.play_arrow_rounded)),
              ),
            ),
          ),
          const SizedBox(height: Space.m),
          Expanded(
            child: _Output(text: output, busy: busy),
          ),
        ],
      ),
    );
  }
}

/// Command output or a log: monospace, selectable, left to right in every language.
class _Output extends StatelessWidget {
  const _Output({required this.text, required this.busy, this.tail = false});
  final String text;
  final bool busy, tail;

  @override
  Widget build(BuildContext context) => Glass(
    padding: const EdgeInsets.all(Space.m),
    child: busy
        ? const Center(child: CircularProgressIndicator())
        : SingleChildScrollView(
            reverse: tail,
            child: Directionality(
              textDirection: TextDirection.ltr,
              child: SelectableText(text, style: Type.mono),
            ),
          ),
  );
}

class LogsPage extends StatefulWidget {
  const LogsPage({super.key, required this.server});
  final ServerEntry server;
  @override
  State<LogsPage> createState() => _LogsPageState();
}

class _LogsPageState extends State<LogsPage> {
  String unit = 'xray-autotune';
  String output = '';
  bool busy = false;

  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addPostFrameCallback((_) => load());
  }

  Future<void> load() async {
    setState(() => busy = true);
    try {
      final r = await AppScope.of(context).manage('manage.logs', widget.server, {'unit': unit, 'lines': 300});
      output = (r as Map)['output'] as String? ?? '';
    } catch (e) {
      output = '$e';
    }
    if (mounted) setState(() => busy = false);
  }

  @override
  Widget build(BuildContext context) {
    const units = ['xray-autotune', 'xray-autotune-sub', 'xray-autotune-tunnels', 'xray-autotune-awg'];
    return XPage.fill(
      title: T.s('logs'),
      actions: [RoundIconButton(icon: Icons.refresh_rounded, tooltip: T.s('refresh'), onPressed: busy ? null : load)],
      body: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Pills(
            labels: const ['xray', 'sub', 'tunnels', 'awg'],
            selected: units.indexOf(unit),
            onSelect: (i) {
              unit = units[i];
              load();
            },
          ),
          const SizedBox(height: Space.m),
          Expanded(
            child: _Output(text: output, busy: busy, tail: true),
          ),
        ],
      ),
    );
  }
}
