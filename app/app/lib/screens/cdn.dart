import 'package:material_ui/material_ui.dart';
import 'package:url_launcher/url_launcher.dart';

import '../main.dart';
import '../state/models.dart';
import '../ui/glass.dart';
import '../ui/i18n.dart';
import 'tune.dart';

/// The token-creation page with exactly the permissions xtune uses (as `xtune cloudflare token`
/// prints).
const cfTokenUrl =
    'https://dash.cloudflare.com/profile/api-tokens?permissionGroupKeys=%5B%7B%22key%22%3A%22zone%22%2C%22type%22%3A%22read%22%7D%2C%7B%22key%22%3A%22dns%22%2C%22type%22%3A%22edit%22%7D%2C%7B%22key%22%3A%22zone_settings%22%2C%22type%22%3A%22edit%22%7D%2C%7B%22key%22%3A%22workers_scripts%22%2C%22type%22%3A%22edit%22%7D%2C%7B%22key%22%3A%22workers_routes%22%2C%22type%22%3A%22edit%22%7D%2C%7B%22key%22%3A%22account_settings%22%2C%22type%22%3A%22read%22%7D%2C%7B%22key%22%3A%22account_analytics%22%2C%22type%22%3A%22read%22%7D%5D&name=xtune&accountId=%2A&zoneId=all';

/// Domains and the CDN, from the phone: a Cloudflare token saved on the server, then a tune with a
/// zone, which gives the server a direct domain and a CDN-proxied one (the fallback when its IP is
/// blocked), with their DNS records.
class CdnWizardPage extends StatefulWidget {
  const CdnWizardPage({super.key, required this.server, required this.overview});
  final ServerEntry server;
  final Map<String, dynamic> overview;
  @override
  State<CdnWizardPage> createState() => _CdnWizardPageState();
}

class _CdnWizardPageState extends State<CdnWizardPage> {
  final token = TextEditingController();
  late final zone = TextEditingController(text: _zoneOf(widget.overview['domain'] as String? ?? ''));
  String status = '';
  bool hasToken = false;
  bool busy = false;
  String? error;

  static String _zoneOf(String d) {
    final parts = d.split('.');
    return parts.length >= 2 ? parts.sublist(parts.length - 2).join('.') : '';
  }

  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addPostFrameCallback((_) => _status());
  }

  Future<void> _status() async {
    try {
      final r = await AppScope.of(context).manage('manage.xtune', widget.server, {
        'args': ['cloudflare', 'status'],
      });
      status = ((r as Map)['output'] as String? ?? '').trim();
      hasToken = status.contains('token set');
    } catch (e) {
      status = '$e';
    }
    if (mounted) setState(() {});
  }

  Future<void> _saveToken() async {
    setState(() {
      busy = true;
      error = null;
    });
    try {
      await AppScope.of(context).manage('manage.cloudflare.token', widget.server, {'token': token.text.trim()});
      token.clear();
      await _status();
    } catch (e) {
      error = '$e';
    }
    if (mounted) setState(() => busy = false);
  }

  @override
  Widget build(BuildContext context) {
    final cdn = (widget.overview['entries'] as List? ?? []).any((e) => ((e as Map)['kind'] as String? ?? '').startsWith('cdn'));
    return XPage(
      title: T.s('cdnTitle'),
      subtitle: widget.server.name.isNotEmpty ? widget.server.name : widget.server.host,
      back: true,
      children: [
        Glass(child: Text(T.s('cdnHow'), style: Type.secondary)),
        SectionLabel(T.s('cdnStep1')),
        if (hasToken)
          Glass(
            child: Row(
              children: [
                Icon(Icons.check_circle_rounded, color: X.accent),
                const SizedBox(width: Space.m),
                Expanded(child: Text(T.s('cdnTokenSet'), style: Type.body)),
                TextButton(onPressed: () => setState(() => hasToken = false), child: Text(T.s('cdnReplace'))),
              ],
            ),
          )
        else ...[
          OutlinedButton.icon(
            style: OutlinedButton.styleFrom(minimumSize: const Size.fromHeight(48)),
            onPressed: () => launchUrl(Uri.parse(cfTokenUrl), mode: LaunchMode.externalApplication),
            icon: const Icon(Icons.open_in_new_rounded),
            label: Text(T.s('cdnMakeToken')),
          ),
          const SizedBox(height: Space.m),
          TextField(
            controller: token,
            obscureText: true,
            textDirection: TextDirection.ltr,
            decoration: InputDecoration(
              labelText: T.s('cdnToken'),
              helperText: T.s('cdnTokenHint'),
              helperMaxLines: 3,
              errorText: error,
              errorMaxLines: 3,
            ),
          ),
          const SizedBox(height: Space.m),
          FilledButton(
            style: FilledButton.styleFrom(minimumSize: const Size.fromHeight(48)),
            onPressed: busy ? null : _saveToken,
            child: busy ? const SizedBox(width: 18, height: 18, child: CircularProgressIndicator(strokeWidth: 2)) : Text(T.s('cdnSaveToken')),
          ),
        ],
        SectionLabel(T.s('cdnStep2')),
        TextField(
          controller: zone,
          textDirection: TextDirection.ltr,
          keyboardType: TextInputType.url,
          decoration: InputDecoration(labelText: T.s('cdnZone'), hintText: 'example.com', helperText: T.s('cdnZoneHint'), helperMaxLines: 3),
        ),
        const SizedBox(height: Space.m),
        FilledButton.icon(
          style: FilledButton.styleFrom(minimumSize: const Size.fromHeight(52)),
          onPressed: !hasToken
              ? null
              : () => pushPage(
                  context,
                  AppScope(
                    state: AppScope.of(context),
                    child: TunePage(server: widget.server, overview: widget.overview, zone: zone.text.trim()),
                  ),
                ),
          icon: const Icon(Icons.auto_fix_high_rounded),
          label: Text(cdn ? T.s('cdnRetune') : T.s('cdnTune')),
        ),
        if (status.isNotEmpty) ...[
          SectionLabel(T.s('cdnStatus')),
          Glass(
            padding: const EdgeInsets.all(Space.m),
            child: Directionality(
              textDirection: TextDirection.ltr,
              child: SelectableText(status, style: Type.mono),
            ),
          ),
        ],
      ],
    );
  }
}
