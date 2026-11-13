import 'dart:io';

import 'package:flutter_secure_storage/flutter_secure_storage.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:shared_preferences/shared_preferences.dart';
import 'package:material_ui/material_ui.dart';
import 'package:xtune_client/core/core_client.dart';
import 'package:xtune_client/main.dart';
import 'package:xtune_client/screens/lock.dart';
import 'package:xtune_client/screens/manage.dart';
import 'package:xtune_client/screens/tools.dart';
import 'package:xtune_client/state/app_state.dart';
import 'package:xtune_client/state/models.dart';
import 'package:xtune_client/ui/glass.dart';
import 'package:xtune_client/ui/i18n.dart';

import 'tune_page_test.dart' show loadFonts;

/// A core that answers from a table, and remembers what it was asked.
class TableCore extends CoreClient {
  TableCore(this.answers);
  final Map<String, dynamic Function(Map<String, dynamic>)> answers;
  final calls = <(String, Map<String, dynamic>)>[];
  @override
  Future<dynamic> call(String method, [Map<String, dynamic> args = const {}]) async {
    calls.add((method, args));
    final f = answers[method];
    return f == null ? null : f(args);
  }
}

final shots = Platform.environment['SHOTS'] != null;

Future<void> phone(WidgetTester t, {double width = 1170}) async {
  t.view.physicalSize = Size(width, 2532);
  t.view.devicePixelRatio = 3;
}

Future<void> settle(WidgetTester t) async {
  for (var i = 0; i < 12; i++) {
    await t.pump(const Duration(milliseconds: 100));
  }
}

Map<String, dynamic> users() => {
  'name': 'ann',
  'status': 'active',
  'enabled': true,
  'up': 1 << 30,
  'down': 40 << 30,
  'quota': 50 << 30,
  'expires': DateTime.now().add(const Duration(days: 20)).millisecondsSinceEpoch ~/ 1000,
  'devices': 2,
  'online': ['198.51.100.7'],
  'blocked': ['198.51.100.9'],
  'daily': {
    for (var i = 0; i < 30; i++)
      () {
        final d = DateTime.now().subtract(Duration(days: i));
        return '${d.year}-${d.month.toString().padLeft(2, '0')}-${d.day.toString().padLeft(2, '0')}';
      }(): (i * 7919 % 13 + 1) << 28,
  },
};

void main() {
  setUpAll(() async {
    FlutterSecureStorage.setMockInitialValues({});
    await loadFonts();
  });
  tearDown(() {
    T.lang = 'en';
    Lock.unlocked = false;
  });

  testWidgets('first run: the guide, then the app', (t) async {
    await phone(t);
    SharedPreferences.setMockInitialValues({});
    final s = AppState(TableCore({}));
    await t.runAsync(s.openPrefsForTest);
    await t.pumpWidget(XtuneApp(state: s));
    await settle(t);
    expect(find.text('Welcome to xtune'), findsOneWidget);
    if (shots) await expectLater(find.byType(MaterialApp), matchesGoldenFile('shots/onboarding.png'));
    await t.tap(find.text('Get started'));
    await settle(t);
    expect(find.text('Add your subscription'), findsOneWidget);
    await t.tap(find.text('Skip'));
    await t.runAsync(() => Future<void>.delayed(const Duration(milliseconds: 100)));
    await settle(t);
    expect(s.settings.onboarded, isTrue);
    expect(find.text('Welcome to xtune'), findsNothing);
    await t.pumpWidget(const SizedBox());
  });

  testWidgets('app lock: a wrong PIN stays locked, the right one opens', (t) async {
    await phone(t);
    final s = AppState(TableCore({}))
      ..settings.onboarded = true
      ..settings.lock = 'app';
    await t.runAsync(() => Lock.setPin('2468'));
    Lock.unlocked = false;
    await t.pumpWidget(XtuneApp(state: s));
    await settle(t);
    expect(find.text('xtune is locked'), findsOneWidget);
    if (shots) await expectLater(find.byType(MaterialApp), matchesGoldenFile('shots/lock.png'));
    Future<void> enter(String pin) async {
      for (final d in pin.split('')) {
        await t.tap(find.bySemanticsLabel(d));
        await t.pump();
      }
      await t.tap(find.bySemanticsLabel('Open'));
      await t.runAsync(() => Future<void>.delayed(const Duration(milliseconds: 600)));
      await settle(t);
    }

    await enter('1357');
    expect(find.text('Wrong PIN'), findsOneWidget);
    await enter('2468');
    expect(find.text('xtune is locked'), findsNothing);
    expect(Lock.unlocked, isTrue);
    await t.pumpWidget(const SizedBox());
  });

  testWidgets('users: chart and devices, kick, bulk add, filters', (t) async {
    await phone(t);
    final core = TableCore({
      'manage.users': (_) => [
        users(),
        {...users(), 'name': 'bob', 'up': 0, 'down': 1 << 20, 'online': [], 'blocked': [], 'daily': null},
      ],
      'manage.user': (a) => {'name': a['name'], 'subscription': 'https://sub.example/p/${a['name']}'},
    });
    final s = AppState(core);
    final srv = ServerEntry(id: 'a', name: 'Frankfurt', host: '203.0.113.7', hostKey: 'k');
    await t.pumpWidget(
      AppScope(
        state: s,
        child: MaterialApp(
          theme: appTheme(),
          home: Scaffold(backgroundColor: Colors.transparent, body: Backdrop(child: UsersPage(server: srv))),
        ),
      ),
    );
    await settle(t);
    expect(find.text('ann'), findsOneWidget);
    await t.tap(find.text('Over 90%'));
    await settle(t);
    expect(find.text('bob'), findsNothing, reason: 'bob has no quota');
    await t.tap(find.text('All'));
    await settle(t);
    if (shots) await expectLater(find.byType(MaterialApp), matchesGoldenFile('shots/users.png'));
    await t.tap(find.text('ann'));
    await settle(t);
    expect(find.text('198.51.100.7'), findsOneWidget);
    expect(find.text('Unblock'), findsOneWidget);
    if (shots) await expectLater(find.byType(MaterialApp), matchesGoldenFile('shots/user-sheet.png'));
    await t.tap(find.text('Kick'));
    await settle(t);
    final kick = core.calls.where((c) => c.$1 == 'manage.user' && c.$2['action'] == 'kick').single.$2;
    expect(kick['ip'], '198.51.100.7');
    expect(kick['name'], 'ann');
    // three at once
    await t.tap(find.byIcon(Icons.person_add_alt_1_rounded));
    await settle(t);
    await t.tap(find.text('Add several'));
    await settle(t);
    await t.enterText(find.widgetWithText(TextField, 'How many'), '3');
    await t.tap(find.text('Add'));
    await settle(t);
    final added = core.calls.where((c) => c.$1 == 'manage.user' && c.$2['action'] == 'add').map((c) => c.$2['name']).toList();
    expect(added, ['user-01', 'user-02', 'user-03']);
    expect(find.text('3 users added'), findsOneWidget);
    await t.pumpWidget(const SizedBox());
  });

  testWidgets('leak test, and the timeline in Persian', (t) async {
    await phone(t);
    final now = DateTime(2026, 11, 13, 14, 5).millisecondsSinceEpoch ~/ 1000;
    final core = TableCore({
      'leak.test': (_) => {
        'exitIp': '203.0.113.50',
        'exitCountry': 'DE',
        'directIp': '5.120.1.2',
        'directCountry': 'IR',
        'tunneled': true,
        'dnsLeak': false,
        'ipv6': 'blocked',
        'resolvers': [
          {'ip': '198.51.100.53', 'country': 'DE', 'asn': 'Cloudflare'},
        ],
      },
      'timeline.list': (_) => [
        {'t': now, 'kind': 'switch', 'text': 'Auto moved to 🇩🇪 r-tcp: hy2 stopped answering'},
        {'t': now - 600, 'kind': 'connect', 'text': 'Connected (VPN) · Auto, starting with 🇩🇪 hy2'},
      ],
    });
    final s = AppState(core)..status = {'running': true};
    Widget host(Widget page) => AppScope(
      state: s,
      child: MaterialApp(
        theme: appTheme(),
        builder: (c, child) => Directionality(textDirection: T.rtl ? TextDirection.rtl : TextDirection.ltr, child: child!),
        home: Scaffold(backgroundColor: Colors.transparent, body: Backdrop(child: page)),
      ),
    );
    await t.pumpWidget(host(const LeakTestPage()));
    await settle(t);
    await t.tap(find.text('Run the test'));
    await settle(t);
    expect(find.textContaining('No leaks'), findsOneWidget);
    expect(find.textContaining('203.0.113.50'), findsOneWidget);
    if (shots) await expectLater(find.byType(MaterialApp), matchesGoldenFile('shots/leak.png'));

    T.lang = 'fa';
    await t.pumpWidget(host(const TimelinePage()));
    await settle(t);
    expect(find.text('۲۲ آبان'), findsOneWidget, reason: 'the day in the Persian calendar');
    expect(find.text('۱۴:۰۵'), findsOneWidget, reason: 'Persian digits');
    if (shots) await expectLater(find.byType(MaterialApp), matchesGoldenFile('shots/timeline-fa.png'));
    await t.pumpWidget(const SizedBox());
  });

  testWidgets('wide screens: users beside the server dashboard', (t) async {
    t.view.physicalSize = const Size(1440, 900);
    t.view.devicePixelRatio = 1;
    final core = TableCore({
      'manage.overview': (_) => {
        'host': '203.0.113.7',
        'version': '1.2.0',
        'country': 'de',
        'entries': [
          {'id': 'r-tcp', 'kind': 'reality-tcp', 'port': 443},
          {'id': 'cdn-xhttp', 'kind': 'cdn-xhttp', 'port': 2053},
        ],
        'users': 2,
        'online': 1,
        'todayBytes': 3 << 30,
        'monthBytes': 80 << 30,
      },
      'manage.users': (_) => [users()],
    });
    final s = AppState(core)
      ..settings.onboarded = true
      ..servers = [ServerEntry(id: 'a', name: 'Frankfurt', host: '203.0.113.7', hostKey: 'k')];
    await t.pumpWidget(XtuneApp(state: s));
    await settle(t);
    await t.tap(find.text('Manage').first);
    await settle(t);
    await settle(t);
    expect(find.text('Users'), findsWidgets);
    expect(find.text('ann'), findsOneWidget, reason: 'the users pane is beside the dashboard');
    expect(find.text('Domains'), findsOneWidget);
    if (shots) await expectLater(find.byType(MaterialApp), matchesGoldenFile('shots/wide-manage.png'));
    await t.pumpWidget(const SizedBox());
  });
}
