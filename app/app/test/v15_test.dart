import 'package:flutter_secure_storage/flutter_secure_storage.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:material_ui/material_ui.dart';
import 'package:shared_preferences/shared_preferences.dart';
import 'package:xtune_client/main.dart';
import 'package:xtune_client/screens/tune.dart';
import 'package:xtune_client/state/app_state.dart';
import 'package:xtune_client/state/models.dart';
import 'package:xtune_client/ui/power_button.dart';
import 'package:xtune_client/ui/route_map.dart';

import 'tune_page_test.dart' show loadFonts;
import 'v13_test.dart' show TableCore, phone, settle, shots, users;

/// A subscription for the screenshots: the test fonts have no flag emoji, so routes are named
/// with country codes ("IR→NL").
Sub sub() => Sub(
  id: 's',
  url: 'https://sub.example/p/token',
  fetched: DateTime.now(),
  profile: {
    'name': 'Frankfurt · ann',
    'country': 'ir',
    'network': 'MCI',
    'usage': {
      'up': 2 << 30,
      'down': 16 << 30,
      'quota': 50 << 30,
      'devices': 2,
      'expires': DateTime.now().add(const Duration(days: 19)).millisecondsSinceEpoch ~/ 1000,
    },
    'configs': [
      {'id': 'a', 'name': 'IR→NL Reality', 'label': 'REALITY TCP', 'kind': 'reality-tcp', 'engine': 'xray', 'server': '198.51.100.1'},
      {'id': 'b', 'name': 'IR→DE XHTTP', 'label': 'CDN XHTTP', 'kind': 'cdn-xhttp', 'engine': 'xray', 'server': '198.51.100.3'},
      {'id': 'e', 'name': 'IR→FR Hysteria2', 'label': 'Hysteria2', 'kind': 'hysteria2', 'engine': 'xray', 'server': '198.51.100.4'},
      {'id': 'c', 'name': 'IR→FI WS', 'label': 'VLESS WS', 'kind': 'ws', 'engine': 'xray', 'server': '198.51.100.2'},
      {'id': 'd', 'name': 'SSH over TLS', 'label': 'SSH', 'kind': 'ssh-tls', 'engine': 'ssh', 'server': '203.0.113.7'},
    ],
  },
);

Map<String, dynamic> overview() => {
  'host': '203.0.113.7',
  'version': '1.5.0',
  'country': 'de',
  'entries': [
    {'id': 'r-tcp', 'kind': 'reality-tcp', 'port': 443},
    {'id': 'cdn-xhttp', 'kind': 'cdn-xhttp', 'port': 2053},
    {'id': 'hy2', 'kind': 'hysteria2', 'port': 8443},
  ],
  'users': 2,
  'online': 1,
  'todayBytes': 3 << 30,
  'monthBytes': 80 << 30,
};

List<Map<String, dynamic>> checkResults() => [
  {'id': 'a', 'name': 'IR→NL Reality', 'state': 'ok', 'warmMs': 120, 'downMbps': 38.0},
  {'id': 'b', 'name': 'IR→DE XHTTP', 'state': 'ok', 'warmMs': 90, 'downMbps': 22.4},
  {'id': 'e', 'name': 'IR→FR Hysteria2', 'state': 'throttled', 'warmMs': 150, 'earlyMbps': 40.0, 'lateMbps': 3.1},
  {'id': 'c', 'name': 'IR→FI WS', 'state': 'blocked', 'detail': 'connection reset by peer'},
  {'id': 'd', 'name': 'SSH over TLS', 'state': 'testing'},
];

/// The app with a subscription in use, a server, and a check that has results.
Future<(AppState, TableCore)> app(WidgetTester t) async {
  SharedPreferences.setMockInitialValues({});
  final core = TableCore({
    'net.rtt': (a) => {for (final h in a['hosts'] as List) h: h == '198.51.100.1' ? 18 : 64},
    'manage.overview': (_) => overview(),
    'manage.users': (_) => [
      users(),
      {...users(), 'name': 'bob', 'up': 0, 'down': 9 << 30, 'online': [], 'blocked': [], 'daily': null},
    ],
    'profile.fetch': (_) => sub().profile,
  });
  final s = AppState(core)
    ..settings.onboarded = true
    ..subs = [sub()]
    ..servers = [ServerEntry(id: 'a', name: 'Frankfurt', host: '203.0.113.7', hostKey: 'k')]
    ..network = {'name': 'MCI', 'asn': 'AS197207'}
    ..checkSubId = 's'
    ..check = {'id': 'c1', 'done': false, 'results': checkResults()}
    ..status = {
      'running': true,
      'since': DateTime.now().millisecondsSinceEpoch ~/ 1000 - 754,
      'active': 'a',
      'outbounds': [
        {'id': 'a', 'alive': true, 'delayMs': 120},
        {'id': 'b', 'alive': true, 'delayMs': 90},
        {'id': 'e', 'alive': true, 'delayMs': 150},
        {'id': 'c', 'alive': false, 'delayMs': 0},
      ],
    };
  await t.runAsync(s.openPrefsForTest);
  await t.pumpWidget(XtuneApp(state: s));
  await settle(t);
  await t.runAsync(() => Future<void>.delayed(const Duration(milliseconds: 50)));
  await settle(t);
  return (s, core);
}

Future<void> tab(WidgetTester t, String name) async {
  await t.tap(find.text(name).last);
  await settle(t);
  await settle(t);
}

Future<void> shot(String name) async {
  if (shots) await expectLater(find.byType(MaterialApp), matchesGoldenFile('shots/$name.png'));
}

void main() {
  setUpAll(() async {
    FlutterSecureStorage.setMockInitialValues({});
    await loadFonts();
  });

  testWidgets('tune has a back button, and it goes back', (t) async {
    await phone(t);
    final (s, _) = await app(t);
    await tab(t, 'Manage');
    expect(find.byTooltip('Back'), findsNothing, reason: 'a tab is not pushed');
    await t.tap(find.text('Tune'));
    await settle(t);
    expect(find.byType(TunePage), findsOneWidget);
    expect(find.byTooltip('Back'), findsOneWidget);
    await shot('tune');
    await t.tap(find.byTooltip('Back'));
    await settle(t);
    expect(find.byType(TunePage), findsNothing);
    s.status = {};
    await t.pumpWidget(const SizedBox());
  });

  testWidgets('home: refresh and remove subscriptions', (t) async {
    await phone(t);
    final (s, core) = await app(t);
    await shot('home');
    // the header refreshes the subscription in use
    await t.tap(find.byTooltip('Refresh subscription').first);
    await t.runAsync(() => Future<void>.delayed(const Duration(milliseconds: 50)));
    await settle(t);
    expect(core.calls.where((c) => c.$1 == 'profile.fetch'), hasLength(1));
    expect(find.text('Frankfurt · ann updated'), findsOneWidget);

    // the picker: each subscription can be refreshed or removed
    s.subs.add(Sub(id: 't', url: 'https://other.example/p/x', fetched: DateTime.now(), profile: {'name': 'Backup', 'configs': []}));
    await t.tap(find.text('Frankfurt · ann').first);
    await settle(t);
    expect(find.byTooltip('Refresh subscription'), findsNWidgets(3), reason: 'the header and one per subscription');
    expect(find.byTooltip('Remove subscription'), findsNWidgets(2));
    await shot('home-subs');
    final running = Map<String, dynamic>.from(s.status)..['running'] = true;
    s.status = running;
    await t.tap(find.byTooltip('Remove subscription').first);
    await settle(t);
    expect(find.text('Remove Frankfurt · ann? Its configs go with it. You can add it again from its link.'), findsOneWidget);
    await t.tap(find.text('Remove'));
    await t.runAsync(() => Future<void>.delayed(const Duration(milliseconds: 50)));
    await settle(t);
    expect(s.subs.map((x) => x.id), ['t']);
    expect(s.sub?.id, 't', reason: 'the next one is in use');
    expect(core.calls.where((c) => c.$1 == 'vpn.stop'), isNotEmpty, reason: 'it was connected through the removed one');
    expect(find.text('Backup'), findsWidgets, reason: 'the picker stays open with the rest');
    s.status = {};
    await t.pumpWidget(const SizedBox());
  });

  testWidgets('phone: every tab is one column', (t) async {
    await phone(t);
    final (s, _) = await app(t);
    expect(t.getTopLeft(find.byType(RouteMap)).dy, greaterThan(t.getBottomLeft(find.byType(PowerButton)).dy), reason: 'under the button');
    await t.drag(find.byType(Scrollable).first, const Offset(0, -560));
    await settle(t);
    await shot('route');
    await t.drag(find.byType(Scrollable).first, const Offset(0, 560));
    await settle(t);
    await tab(t, 'Configs');
    await shot('configs');
    await tab(t, 'Check');
    await shot('check');
    await tab(t, 'Settings');
    await shot('settings');
    await tab(t, 'Manage');
    await shot('manage');
    s.status = {};
    await t.pumpWidget(const SizedBox());
  });

  testWidgets('wide: every tab has the same frame, with a side pane', (t) async {
    t.view.physicalSize = const Size(1440, 900);
    t.view.devicePixelRatio = 1;
    final (s, _) = await app(t);
    // the frame is centred and the same on every tab: (1440 - 1240) / 2 = 100
    const left = 100.0;
    double x(Finder f) => t.getTopLeft(f.first).dx;

    expect(x(find.text('xtune')), left);
    expect(x(find.byType(RouteMap)), greaterThan(t.getTopRight(find.byType(PowerButton)).dx), reason: 'the route beside the button');
    await shot('wide-home');

    await tab(t, 'Configs');
    expect(x(find.text('Configs')), left);
    await shot('wide-configs');
    expect(find.text('Test'), findsOneWidget, reason: 'the config in use, in the side pane');

    await tab(t, 'Check');
    expect(x(find.text('Check this network')), left);
    expect(x(find.text('Stop')), greaterThan(x(find.text('IR→FI WS'))), reason: 'the summary beside the results');
    await shot('wide-check');

    await tab(t, 'Manage');
    expect(find.text('bob'), findsOneWidget, reason: 'the users beside the dashboard');
    expect(x(find.text('Users')), greaterThan(x(find.text('Domains'))));
    await shot('wide-manage');

    await tab(t, 'Settings');
    expect(x(find.text('Settings')), left);
    expect(x(find.text('LOCAL PROXY')), greaterThan(x(find.text('APPEARANCE'))), reason: 'two columns of sections');
    await shot('wide-settings');
    s.status = {};
    await t.pumpWidget(const SizedBox());
  });
}
