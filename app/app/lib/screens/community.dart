import 'package:flutter/services.dart';
import 'package:material_ui/material_ui.dart';

import '../main.dart';
import '../state/app_state.dart';
import '../state/models.dart';
import '../ui/glass.dart';
import '../ui/i18n.dart';

/// A country's flag from its code ("DE" → 🇩🇪), or "" for none.
String flagOf(String? cc) {
  if (cc == null || !RegExp(r'^[A-Za-z]{2}$').hasMatch(cc)) return '';
  final up = cc.toUpperCase();
  return String.fromCharCodes([0x1F1E6 + up.codeUnitAt(0) - 65, 0x1F1E6 + up.codeUnitAt(1) - 65]);
}

/// The directory's refusal in words: its code ("full", "busy", …) is at the end of the core's error.
String communityError(Object e) {
  final m = '$e';
  if (m.contains('no directory is set up')) return T.s('community_notSet');
  final code = RegExp(r'community: (?:.* \()?([a-zA-Z]+)\)?$').firstMatch(m.trim())?.group(1);
  final key = 'community_$code';
  final t = T.s(key);
  return t == key ? m.replaceFirst('community: ', '') : t;
}

/// Opens the community directory.
Future<void> openCommunity(BuildContext context) => pushPage(context, AppScope(state: AppScope.of(context), child: const CommunityPage()));

/// The VPNs people shared with everyone: from the xtune app and from @chthvpnbot. Joining an xtune
/// pool gives this device its own user; it becomes the subscription in use.
class CommunityPage extends StatefulWidget {
  const CommunityPage({super.key});
  @override
  State<CommunityPage> createState() => _CommunityPageState();
}

class _CommunityPageState extends State<CommunityPage> {
  List<Map<String, dynamic>>? vpns;
  String? error;
  String country = '';
  final busy = <String>{};

  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addPostFrameCallback((_) => load());
  }

  Future<void> load() async {
    setState(() => error = null);
    try {
      final l = await AppScope.of(context).communityList();
      if (mounted) setState(() => vpns = l);
    } catch (e) {
      if (mounted) setState(() => error = communityError(e));
    }
  }

  void _say(String text) {
    if (mounted) ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text(text)));
  }

  Future<void> _join(AppState s, Map<String, dynamic> v) async {
    final id = v['id'] as String;
    setState(() => busy.add(id));
    try {
      final sub = await s.joinCommunity(v);
      _say(T.s('communityJoined', {'name': sub.name}));
      if (mounted) Navigator.of(context).maybePop();
    } catch (e) {
      _say(communityError(e));
    } finally {
      if (mounted) setState(() => busy.remove(id));
    }
  }

  Future<void> _leave(AppState s, Sub sub) async {
    final ok = await showXSheet<bool>(
      context,
      title: T.s('communityLeave'),
      builder: (ctx, _) => Column(
        mainAxisSize: MainAxisSize.min,
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Text(T.s('communityLeaveHint', {'name': sub.name}), style: Type.secondary),
          const SizedBox(height: Space.l),
          FilledButton(
            style: FilledButton.styleFrom(backgroundColor: X.bad),
            onPressed: () => Navigator.pop(ctx, true),
            child: Text(T.s('communityLeave')),
          ),
        ],
      ),
    );
    if (ok != true) return;
    await s.leaveCommunity(sub);
    if (mounted) setState(() {});
  }

  Future<void> _report(AppState s, Map<String, dynamic> v) async {
    final reason = await showXSheet<String>(
      context,
      title: T.s('communityReport'),
      padded: false,
      builder: (ctx, _) => Column(
        mainAxisSize: MainAxisSize.min,
        children: [
          for (final r in ['dead', 'scam', 'illegal']) ListTile(title: Text(T.s('communityReport_$r')), onTap: () => Navigator.pop(ctx, r)),
        ],
      ),
    );
    if (reason == null) return;
    try {
      await s.reportCommunity(v['id'] as String, reason);
      _say(T.s('communityReported'));
    } catch (e) {
      _say(communityError(e));
    }
  }

  @override
  Widget build(BuildContext context) {
    final s = AppScope.of(context);
    final all = vpns ?? [];
    final countries = {
      for (final v in all)
        if ((v['country'] as String?)?.isNotEmpty == true) v['country'] as String,
    }.toList()..sort();
    final shown = country.isEmpty ? all : all.where((v) => v['country'] == country).toList();
    return XPage(
      title: T.s('community'),
      subtitle: T.s('communityHint'),
      back: true,
      onRefresh: load,
      actions: [RoundIconButton(icon: Icons.refresh_rounded, tooltip: T.s('refresh'), onPressed: load)],
      children: [
        if (error != null) Notice(error!),
        if (vpns == null && error == null)
          const Padding(
            padding: EdgeInsets.all(40),
            child: Center(child: CircularProgressIndicator()),
          ),
        if (countries.length > 1) ...[
          Pills(
            labels: [T.s('all'), for (final c in countries) '${flagOf(c)} $c'],
            selected: country.isEmpty ? 0 : countries.indexOf(country) + 1,
            onSelect: (i) => setState(() => country = i == 0 ? '' : countries[i - 1]),
          ),
          const SizedBox(height: Space.m),
        ],
        if (vpns != null && shown.isEmpty) Glass(child: Text(T.s('communityEmpty'), style: Type.secondary)),
        for (final v in shown)
          Padding(
            padding: const EdgeInsets.only(bottom: Space.s),
            child: _VpnCard(
              vpn: v,
              joined: s.communitySub(v['id'] as String),
              busy: busy.contains(v['id']),
              onJoin: () => _join(s, v),
              onOpen: (sub) {
                s.selectSub(sub);
                Navigator.of(context).maybePop();
              },
              onLeave: (sub) => _leave(s, sub),
              onReport: () => _report(s, v),
            ),
          ),
        const SizedBox(height: Space.s),
        Text(T.s('communityPrivacy'), style: Type.caption),
      ],
    );
  }
}

class _VpnCard extends StatelessWidget {
  const _VpnCard({
    required this.vpn,
    required this.joined,
    required this.busy,
    required this.onJoin,
    required this.onOpen,
    required this.onLeave,
    required this.onReport,
  });
  final Map<String, dynamic> vpn;
  final Sub? joined;
  final bool busy;
  final VoidCallback onJoin, onReport;
  final ValueChanged<Sub> onOpen, onLeave;

  @override
  Widget build(BuildContext context) {
    final v = vpn;
    final protocols = ((v['protocols'] as List?) ?? []).cast<String>();
    final max = ((v['max'] as num?) ?? 0).toInt(), active = ((v['active'] as num?) ?? 0).toInt();
    final quota = ((v['quotaGB'] as num?) ?? 0).toDouble(), days = ((v['days'] as num?) ?? 0).toInt();
    final devices = ((v['devices'] as num?) ?? 0).toInt();
    final xtune = v['kind'] == 'xtune';
    final limits = [
      if (xtune) T.s('communityOwnUser'),
      if (quota > 0) T.s('communityQuota', {'gb': quota % 1 == 0 ? quota.toInt() : quota}),
      if (days > 0) T.s('communityDays', {'n': days}),
      if (devices > 0) T.s('communityDevices', {'n': devices}),
    ];
    return Glass(
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Row(
            children: [
              if (flagOf(v['country'] as String?).isNotEmpty) ...[
                Text(flagOf(v['country'] as String?), style: const TextStyle(fontSize: 22)),
                const SizedBox(width: Space.s),
              ],
              Expanded(
                child: Text(v['name'] as String? ?? '', style: Type.bodyStrong, overflow: TextOverflow.ellipsis),
              ),
              Text(max > 0 ? T.s('communitySlots', {'a': active, 'm': max}) : T.s('communityMembers', {'n': active}), style: Type.caption),
              XMenuButton<String>(
                tooltip: T.s('more'),
                round: false,
                onSelected: (k) {
                  if (k == 'report') onReport();
                  if (k == 'leave' && joined != null) onLeave(joined!);
                },
                items: [
                  XMenuItem('report', T.s('communityReport'), icon: Icons.flag_outlined),
                  if (joined != null) XMenuItem('leave', T.s('communityLeave'), icon: Icons.logout_rounded, danger: true),
                ],
              ),
            ],
          ),
          if ((v['description'] as String?)?.isNotEmpty == true) ...[
            const SizedBox(height: Space.xs),
            Text(v['description'] as String, style: Type.secondary),
          ],
          const SizedBox(height: Space.s),
          Wrap(spacing: 6, runSpacing: 6, children: [for (final p in protocols.take(6)) Badge2(p, X.blue)]),
          if (limits.isNotEmpty) ...[const SizedBox(height: Space.s), Text(limits.join(' · '), style: Type.caption)],
          const SizedBox(height: Space.m),
          Align(
            alignment: AlignmentDirectional.centerEnd,
            child: joined != null
                ? FilledButton.tonalIcon(onPressed: () => onOpen(joined!), icon: const Icon(Icons.check_rounded), label: Text(T.s('communityUse')))
                : FilledButton.icon(
                    onPressed: busy ? null : onJoin,
                    icon: busy
                        ? const SizedBox(width: 16, height: 16, child: CircularProgressIndicator(strokeWidth: 2))
                        : const Icon(Icons.add_rounded),
                    label: Text(T.s('communityJoin')),
                  ),
          ),
        ],
      ),
    );
  }
}

// ---------------------------------------------------------------- sharing (Manage)

/// Shares this server with the community: a public pool on it (every member gets their own user
/// with these limits) listed in the directory, the app and @chthvpnbot alike. Below, what this
/// server already shares, with its members and reports.
class SharePage extends StatefulWidget {
  const SharePage({super.key, required this.server, this.overview});
  final ServerEntry server;
  final Map<String, dynamic>? overview;
  @override
  State<SharePage> createState() => _SharePageState();
}

class _SharePageState extends State<SharePage> {
  late final name = TextEditingController(text: widget.server.name.isNotEmpty ? widget.server.name : 'xtune');
  final description = TextEditingController();
  late final country = TextEditingController(text: (widget.overview?['country'] as String? ?? '').toUpperCase());
  final members = TextEditingController(text: '50');
  final quota = TextEditingController(text: '20');
  final days = TextEditingController(text: '30');
  final devices = TextEditingController(text: '2');
  bool sharing = false;
  String? error;
  final status = <String, Map<String, dynamic>>{};
  final statusError = <String, String>{};

  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addPostFrameCallback((_) => loadStatus());
  }

  @override
  void dispose() {
    for (final c in [name, description, country, members, quota, days, devices]) {
      c.dispose();
    }
    super.dispose();
  }

  List<SharedVpn> _mine(AppState s) => s.settings.shared.where((v) => v.serverId == widget.server.id).toList();

  Future<void> loadStatus() async {
    final s = AppScope.of(context);
    for (final v in _mine(s)) {
      try {
        status[v.id] = await s.sharedStatus(v);
        statusError.remove(v.id);
      } catch (e) {
        statusError[v.id] = communityError(e);
      }
      if (mounted) setState(() {});
    }
  }

  /// A pool name the server takes: letters and digits, not one this server already shares.
  String _poolName(AppState s) {
    final taken = _mine(s).map((v) => v.pool).toSet();
    for (var i = 1; ; i++) {
      final n = i == 1 ? 'community' : 'community$i';
      if (!taken.contains(n)) return n;
    }
  }

  Future<void> _share(AppState s) async {
    setState(() {
      sharing = true;
      error = null;
    });
    try {
      await s.shareServer(
        widget.server,
        pool: _poolName(s),
        name: name.text.trim(),
        description: description.text.trim(),
        country: country.text.trim(),
        members: int.tryParse(members.text.trim()) ?? 0,
        quotaGb: double.tryParse(quota.text.trim()) ?? 0,
        days: int.tryParse(days.text.trim()) ?? 0,
        devices: int.tryParse(devices.text.trim()) ?? 0,
      );
      if (mounted) ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text(T.s('shareDone'))));
      await loadStatus();
    } catch (e) {
      final m = '$e';
      error = m.contains('old xtune')
          ? T.s('changeOld')
          : m.contains('no subscription server')
          ? T.s('shareNeedsSub')
          : m.contains('already exists')
          ? T.s('sharePoolExists')
          : communityError(e);
    } finally {
      if (mounted) setState(() => sharing = false);
    }
  }

  Future<void> _unshare(AppState s, SharedVpn v) async {
    var removePool = true;
    final ok = await showXSheet<bool>(
      context,
      title: T.s('unshare'),
      builder: (ctx, setSheet) => Column(
        mainAxisSize: MainAxisSize.min,
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Text(T.s('unshareHint', {'name': v.name}), style: Type.secondary),
          if (v.pool.isNotEmpty)
            CheckboxListTile(
              contentPadding: EdgeInsets.zero,
              value: removePool,
              onChanged: (x) => setSheet(() => removePool = x ?? false),
              title: Text(T.s('unsharePool')),
              subtitle: Text(T.s('unsharePoolHint')),
            ),
          const SizedBox(height: Space.m),
          FilledButton(
            style: FilledButton.styleFrom(backgroundColor: X.bad),
            onPressed: () => Navigator.pop(ctx, true),
            child: Text(T.s('unshare')),
          ),
        ],
      ),
    );
    if (ok != true) return;
    try {
      await s.unshare(v, removePool: removePool);
    } catch (e) {
      if (mounted) ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text(communityError(e))));
    }
    if (mounted) setState(() {});
  }

  Widget _field(TextEditingController c, String label, {String? hint, bool number = false, int lines = 1}) => Padding(
    padding: const EdgeInsets.only(bottom: Space.s),
    child: TextField(
      controller: c,
      maxLines: lines,
      keyboardType: number ? const TextInputType.numberWithOptions(decimal: true) : null,
      inputFormatters: number ? [FilteringTextInputFormatter.allow(RegExp(r'[0-9.]'))] : null,
      decoration: InputDecoration(labelText: label, helperText: hint),
    ),
  );

  @override
  Widget build(BuildContext context) {
    final s = AppScope.of(context);
    final mine = _mine(s);
    return XPage(
      title: T.s('share'),
      subtitle: T.s('shareHint'),
      back: true,
      onRefresh: loadStatus,
      children: [
        for (final v in mine) ...[
          _SharedCard(
            vpn: v,
            status: status[v.id],
            error: statusError[v.id],
            onPause: (p) async {
              try {
                await s.editShared(v, {'paused': p});
                await loadStatus();
              } catch (e) {
                if (context.mounted) ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text(communityError(e))));
              }
            },
            onRemove: () => _unshare(s, v),
          ),
          const SizedBox(height: Space.s),
        ],
        SectionLabel(mine.isEmpty ? T.s('shareNew') : T.s('shareAnother')),
        Glass(
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: [
              _field(name, T.s('shareName')),
              _field(description, T.s('shareDescription'), lines: 2),
              _field(country, T.s('shareCountry'), hint: T.s('shareCountryHint')),
              Row(
                children: [
                  Expanded(child: _field(members, T.s('shareMembers'), hint: T.s('zeroUnlimited'), number: true)),
                  const SizedBox(width: Space.s),
                  Expanded(child: _field(devices, T.s('shareDevices'), number: true)),
                ],
              ),
              Row(
                children: [
                  Expanded(child: _field(quota, T.s('shareQuota'), number: true)),
                  const SizedBox(width: Space.s),
                  Expanded(child: _field(days, T.s('shareDays'), number: true)),
                ],
              ),
              const SizedBox(height: Space.s),
              Text(T.s('shareHow'), style: Type.caption),
              if (error != null) ...[const SizedBox(height: Space.s), Notice(error!)],
              const SizedBox(height: Space.m),
              FilledButton.icon(
                style: FilledButton.styleFrom(minimumSize: const Size.fromHeight(48)),
                onPressed: sharing || name.text.trim().length < 2 ? null : () => _share(s),
                icon: sharing
                    ? const SizedBox(width: 16, height: 16, child: CircularProgressIndicator(strokeWidth: 2))
                    : const Icon(Icons.public_rounded),
                label: Text(T.s('shareButton')),
              ),
            ],
          ),
        ),
      ],
    );
  }
}

class _SharedCard extends StatelessWidget {
  const _SharedCard({required this.vpn, required this.status, required this.error, required this.onPause, required this.onRemove});
  final SharedVpn vpn;
  final Map<String, dynamic>? status;
  final String? error;
  final ValueChanged<bool> onPause;
  final VoidCallback onRemove;

  @override
  Widget build(BuildContext context) {
    final v = Map<String, dynamic>.from((status?['vpn'] as Map?) ?? {});
    final paused = v['paused'] == true;
    final down = v['status'] == 'down', unlisted = v['unlisted'] == true;
    final reports = Map<String, dynamic>.from((status?['reports'] as Map?) ?? {});
    final max = ((v['max'] as num?) ?? 0).toInt(), active = ((v['active'] as num?) ?? 0).toInt();
    final state = error != null
        ? error!
        : status == null
        ? T.s('loading')
        : unlisted
        ? T.s('sharedUnlisted')
        : down
        ? T.s('sharedDown')
        : paused
        ? T.s('sharedPaused')
        : T.s('sharedListed');
    final color = error != null || unlisted || down ? X.bad : (paused ? X.warn : X.accent);
    return Glass(
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Row(
            children: [
              Dot(color),
              const SizedBox(width: Space.s),
              Expanded(
                child: Text(vpn.name, style: Type.bodyStrong, overflow: TextOverflow.ellipsis),
              ),
              if (status != null)
                Text(max > 0 ? T.s('communitySlots', {'a': active, 'm': max}) : T.s('communityMembers', {'n': active}), style: Type.caption),
            ],
          ),
          const SizedBox(height: Space.xs),
          Text(state, style: Type.secondary.copyWith(color: color == X.accent ? null : color)),
          if (reports.isNotEmpty)
            Text(T.s('sharedReports', {'n': reports.values.fold<num>(0, (a, b) => a + (b as num))}), style: Type.caption.copyWith(color: X.warn)),
          if (vpn.pool.isNotEmpty) Text(T.s('sharedPool', {'pool': vpn.pool}), style: Type.caption),
          const SizedBox(height: Space.s),
          Row(
            mainAxisAlignment: MainAxisAlignment.end,
            children: [
              if (status != null)
                TextButton.icon(
                  onPressed: () => onPause(!paused),
                  icon: Icon(paused ? Icons.play_arrow_rounded : Icons.pause_rounded),
                  label: Text(T.s(paused ? 'sharedResume' : 'sharedPause')),
                ),
              TextButton.icon(
                onPressed: onRemove,
                icon: Icon(Icons.delete_outline_rounded, color: X.bad),
                label: Text(T.s('unshare'), style: TextStyle(color: X.bad)),
              ),
            ],
          ),
        ],
      ),
    );
  }
}

/// The community directory's address (Settings): the one this build came with, or another.
Future<void> editCommunityUrl(BuildContext context) async {
  final s = AppScope.of(context);
  final builtIn = (((await s.core.call('community.url')) as Map?)?['url'] as String?) ?? '';
  if (!context.mounted) return;
  final c = TextEditingController(text: s.settings.communityUrl);
  final v = await showXSheet<String>(
    context,
    title: T.s('communityUrl'),
    builder: (ctx, _) => Column(
      mainAxisSize: MainAxisSize.min,
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Text(T.s('communityUrlHint'), style: Type.secondary),
        const SizedBox(height: Space.m),
        TextField(
          controller: c,
          keyboardType: TextInputType.url,
          decoration: InputDecoration(
            labelText: 'https://…',
            helperText: builtIn.isEmpty ? T.s('communityUrlNone') : T.s('communityUrlBuiltIn', {'url': builtIn}),
          ),
        ),
        const SizedBox(height: Space.l),
        FilledButton(onPressed: () => Navigator.pop(ctx, c.text.trim()), child: Text(T.s('save'))),
      ],
    ),
  );
  c.dispose();
  if (v != null) await s.updateSettings((x) => x.communityUrl = v);
}
