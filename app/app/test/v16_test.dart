import 'package:flutter_secure_storage/flutter_secure_storage.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:material_ui/material_ui.dart';
import 'package:shared_preferences/shared_preferences.dart';
import 'package:xtune_client/main.dart';
import 'package:xtune_client/screens/community.dart';
import 'package:xtune_client/screens/manage.dart';
import 'package:xtune_client/screens/options.dart';
import 'package:xtune_client/state/app_state.dart';
import 'package:xtune_client/state/models.dart';

import 'tune_page_test.dart' show loadFonts;
import 'v13_test.dart' show TableCore, phone, settle;
import 'v15_test.dart' show overview, sub;

Future<(AppState, TableCore)> app(WidgetTester t) async {
  SharedPreferences.setMockInitialValues({});
  final now = DateTime.now().millisecondsSinceEpoch;
  final core = TableCore({
    'manage.overview': (a) {
      if (a['id'] == 'b') throw Exception('dial tcp 198.51.100.9:22: i/o timeout');
      final o = Map<String, dynamic>.from(overview());
      final entries = (o['entries'] as List).map((e) => Map<String, dynamic>.from(e as Map)).toList();
      entries[0]
        ..['sni'] = 'www.example.com'
        ..['quiet'] = 3 * 3600;
      o['entries'] = entries;
      return o;
    },
    'manage.users': (_) => [],
    'manage.xtune': (a) => (a['args'] as List).first == 'public'
        ? {'output': '{"name":"community","link":"https://sub.example.com/p/pub/${'a' * 32}","active":0,"max":50}\n'}
        : {'output': '{"id":"r-tcp","changed":["sni www.example.com → www.example.org"],"key":"r-tcp/443","auto":true}\n'},
    'community.request': (a) {
      final path = a['path'] as String;
      if (path == '/v1/vpns' && a['method'] == 'GET') {
        return {
          'vpns': [
            {
              'id': 'k3x9',
              'kind': 'xtune',
              'name': 'Frankfurt free',
              'country': 'DE',
              'protocols': ['VLESS REALITY', 'Hysteria2'],
              'active': 12,
              'max': 50,
              'quotaGB': 20,
              'days': 30,
              'devices': 2,
              'description': 'For students',
            },
            {
              'id': 'm2p7',
              'kind': 'sub',
              'name': 'Helsinki panel',
              'country': 'FI',
              'protocols': ['VLESS'],
              'active': 3,
              'max': 0,
            },
          ],
        };
      }
      if (path == '/v1/vpns/k3x9/join') return {'kind': 'xtune', 'sub': 'https://sub.example.com:2053/p/abcdefghij', 'name': 'Frankfurt free'};
      if (path == '/v1/vpns' && a['method'] == 'POST') return {'id': 'new1', 'ownerKey': 'f' * 32, 'vpn': {}};
      if (path == '/v1/vpns/new1/owner') {
        return {
          'vpn': {'id': 'new1', 'name': 'Frankfurt', 'status': 'ok', 'paused': false, 'active': 0, 'max': 50},
          'appMembers': 0,
          'reports': {},
        };
      }
      return {};
    },
    'profile.import': (a) => {
      'url': a['text'],
      'profile': {'name': 'Frankfurt free · abcdefghij', 'configs': []},
    },
    'crash.list': (_) => [
      {
        'at': now,
        'source': 'android',
        'kind': 'crash',
        'where': 'thread main',
        'title': 'android.os.TransactionTooLargeException: data parcel size 812344 bytes',
        'text': 'android.os.TransactionTooLargeException: data parcel size 812344 bytes\n\tat android.os.BinderProxy.transactNative',
      },
      {'at': now - 1000, 'source': 'go', 'kind': 'error', 'where': 'smart reconnect', 'title': 'panic: boom', 'text': 'panic: boom'},
    ],
  });
  final s = AppState(core)
    ..settings.onboarded = true
    ..subs = [sub()]
    ..servers = [
      ServerEntry(id: 'a', name: 'Frankfurt', host: '203.0.113.7', hostKey: 'k'),
      ServerEntry(id: 'b', name: 'Helsinki', host: '198.51.100.9', hostKey: 'k'),
    ];
  await t.runAsync(s.openPrefsForTest);
  await t.pumpWidget(XtuneApp(state: s));
  await settle(t);
  return (s, core);
}

Future<void> tab(WidgetTester t, String name) async {
  await t.tap(find.text(name).last);
  await settle(t);
  await t.runAsync(() => Future<void>.delayed(const Duration(milliseconds: 50)));
  await settle(t);
}

void main() {
  setUpAll(() async {
    FlutterSecureStorage.setMockInitialValues({});
    await loadFonts();
  });

  testWidgets('all servers: each one at a glance, the one down said so', (t) async {
    await phone(t);
    final (s, _) = await app(t);
    await tab(t, 'Manage');
    await t.tap(find.byTooltip('All servers'));
    await settle(t);
    await t.runAsync(() => Future<void>.delayed(const Duration(milliseconds: 100)));
    await settle(t);
    expect(find.byType(AllServersPage), findsOneWidget);
    expect(find.text('Helsinki'), findsOneWidget);
    expect(find.textContaining('i/o timeout'), findsOneWidget);
    expect(find.textContaining('1 not answering'), findsOneWidget);
    expect(find.text('1 of 3 configs look blocked'), findsOneWidget, reason: 'r-tcp was quiet for 3 hours');
    await t.tap(find.text('Helsinki'));
    await settle(t);
    expect(s.server?.id, 'b');
    await t.pumpWidget(const SizedBox());
  });

  testWidgets('manage: change a config that looks blocked', (t) async {
    await phone(t);
    final (_, core) = await app(t);
    await tab(t, 'Manage');
    expect(find.text('no traffic for 3 h: probably blocked'), findsOneWidget);
    await t.tap(find.text('r-tcp'));
    await settle(t);
    expect(find.text('Change r-tcp'), findsOneWidget);
    expect(find.text('New target (SNI)'), findsOneWidget);
    await t.tap(find.text('Change').first);
    await t.runAsync(() => Future<void>.delayed(const Duration(milliseconds: 50)));
    await settle(t);
    final call = core.calls.lastWhere((c) => c.$1 == 'manage.xtune');
    expect(call.$2['args'], ['change', 'r-tcp', '--json']);
    expect(find.textContaining('sni www.example.com → www.example.org'), findsOneWidget);
    expect(find.textContaining('next subscription update'), findsOneWidget);
    await t.pumpWidget(const SizedBox());
  });

  testWidgets('settings: the crash log', (t) async {
    await phone(t);
    final (s, _) = await app(t);
    await t.runAsync(s.refreshCrashCount);
    await tab(t, 'Settings');
    await t.scrollUntilVisible(find.text('Crash log'), 300, scrollable: find.byType(Scrollable).first);
    await t.drag(find.byType(Scrollable).first, const Offset(0, -300)); // out from under the tab bar
    await settle(t);
    expect(find.text('2 new'), findsOneWidget);
    expect(find.text('Log level'), findsOneWidget);
    await t.tap(find.text('Crash log'));
    await t.runAsync(() => Future<void>.delayed(const Duration(milliseconds: 50)));
    await settle(t);
    expect(find.byType(CrashLogPage), findsOneWidget);
    expect(find.textContaining('TransactionTooLargeException'), findsOneWidget);
    expect(find.textContaining('closed the app · android · thread main'), findsOneWidget);
    expect(s.newCrashes, 0, reason: 'opening the log marks them seen');
    await t.pumpWidget(const SizedBox());
  });

  testWidgets('community: browse from Configs, join, and it is the subscription in use', (t) async {
    await phone(t);
    final (s, core) = await app(t);
    await tab(t, 'Configs');
    await t.tap(find.text('Community VPNs'));
    await t.runAsync(() => Future<void>.delayed(const Duration(milliseconds: 50)));
    await settle(t);
    expect(find.byType(CommunityPage), findsOneWidget);
    expect(find.text('Frankfurt free'), findsOneWidget);
    expect(find.text('12/50 members'), findsOneWidget);
    expect(find.textContaining('your own account · 20 GB · 30 days · 2 devices'), findsOneWidget);
    await t.tap(find.text('Join').first);
    await t.runAsync(() => Future<void>.delayed(const Duration(milliseconds: 100)));
    await settle(t);
    final join = core.calls.firstWhere((c) => c.$1 == 'community.request' && c.$2['path'] == '/v1/vpns/k3x9/join');
    expect((join.$2['body'] as Map)['member'], matches(RegExp(r'^[0-9a-f]{32}$')));
    final joined = s.communitySub('k3x9');
    expect(joined, isNotNull);
    expect(joined!.url, 'https://sub.example.com:2053/p/abcdefghij');
    expect(s.sub?.id, joined.id, reason: 'the joined one is in use');
    // the same device asks with the same key next time
    final key = await t.runAsync(s.communityMember);
    expect(key, (join.$2['body'] as Map)['member']);
    await t.pumpWidget(const SizedBox());
  });

  testWidgets('home: the subscription picker opens the community', (t) async {
    await phone(t);
    await app(t);
    await t.tap(find.text('Frankfurt · ann').first);
    await settle(t);
    await t.tap(find.text('Community VPNs'));
    await t.runAsync(() => Future<void>.delayed(const Duration(milliseconds: 50)));
    await settle(t);
    expect(find.byType(CommunityPage), findsOneWidget);
    expect(find.text('Helsinki panel'), findsOneWidget);
    await t.pumpWidget(const SizedBox());
  });

  testWidgets('manage: share a server with the community', (t) async {
    await phone(t);
    final (s, core) = await app(t);
    await tab(t, 'Manage');
    await t.scrollUntilVisible(find.text('Share'), 200, scrollable: find.byType(Scrollable).first);
    await t.drag(find.byType(Scrollable).first, const Offset(0, -200));
    await settle(t);
    await t.tap(find.text('Share'));
    await settle(t);
    expect(find.byType(SharePage), findsOneWidget);
    expect(find.text('DE'), findsOneWidget, reason: 'the country comes from the server');
    await t.scrollUntilVisible(find.text('Share with the community'), 200, scrollable: find.byType(Scrollable).first);
    await t.drag(find.byType(Scrollable).first, const Offset(0, -200));
    await settle(t);
    await t.tap(find.text('Share with the community'));
    await t.runAsync(() => Future<void>.delayed(const Duration(milliseconds: 100)));
    await settle(t);
    final add = core.calls.firstWhere((c) => c.$1 == 'manage.xtune' && (c.$2['args'] as List).first == 'public');
    expect(add.$2['args'], ['public', 'add', 'community', '--json', '--members', '50', '--quota', '20.0', '--days', '30', '--devices', '2']);
    final post = core.calls.firstWhere((c) => c.$1 == 'community.request' && c.$2['method'] == 'POST');
    expect((post.$2['body'] as Map)['source'], 'https://sub.example.com/p/pub/${'a' * 32}');
    expect((post.$2['body'] as Map)['country'], 'DE');
    expect(s.settings.shared.single.id, 'new1');
    expect(s.settings.shared.single.pool, 'community');
    expect(find.text('Listed'), findsOneWidget);
    await t.pumpWidget(const SizedBox());
  });
}
