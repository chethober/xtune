import 'package:flutter_secure_storage/flutter_secure_storage.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:material_ui/material_ui.dart';
import 'package:shared_preferences/shared_preferences.dart';
import 'package:xtune_client/main.dart';
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
    'manage.xtune': (a) => {
      'output': '{"id":"r-tcp","changed":["sni www.example.com → www.example.org"],"key":"r-tcp/443","auto":true}\n',
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
}
