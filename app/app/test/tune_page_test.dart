import 'dart:io';

import 'package:flutter/services.dart';
import 'package:flutter_secure_storage/flutter_secure_storage.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:material_ui/material_ui.dart';
import 'package:xtune_client/core/core_client.dart';
import 'package:xtune_client/main.dart';
import 'package:xtune_client/screens/tune.dart';
import 'package:xtune_client/state/app_state.dart';
import 'package:xtune_client/state/models.dart';
import 'package:xtune_client/ui/glass.dart';

/// A core that plays a tune: prepared, measured, deployed.
class FakeCore extends CoreClient {
  final calls = <String, Map<String, dynamic>>{};
  String phase = '';
  @override
  Future<dynamic> call(String method, [Map<String, dynamic> args = const {}]) async {
    calls[method] = args;
    switch (method) {
      case 'manage.tune.start':
        phase = 'measuring';
        return _job('preparing');
      case 'manage.tune.status':
        if ((args['id'] as String? ?? '').isEmpty) return {'phase': ''};
        return _job(phase);
      case 'manage.tune.deploy':
        phase = 'done';
        return _job('deploying');
    }
    return null;
  }

  Map<String, dynamic> _job(String p) => {
    'id': 't1',
    'server': '203.0.113.7',
    'phase': p == 'measuring' ? 'measured' : p,
    'total': 3,
    'done': 3,
    'skipped': ['ovpn:1194'],
    'results': [
      {'id': 'r-tcp#0', 'key': 'r-tcp:443', 'name': 'r-tcp:443 fp=chrome', 'state': 'ok', 'ok': 3, 'n': 3, 'warmMs': 180, 'downMbps': 24.5},
      {'id': 'ssh#1', 'key': 'ssh:2222', 'name': 'ssh:2222', 'state': 'slow', 'ok': 3, 'n': 3, 'warmMs': 420, 'downMbps': 0.8},
      {'id': 'hy2#2', 'key': 'hy2:8443', 'name': 'hy2:8443', 'state': 'blocked', 'ok': 0, 'n': 3, 'warmMs': -1, 'detail': 'timeout'},
    ],
    'scores': [37.1, 6.0, 0],
    if (p == 'done') 'output': 'Chosen for 1 network(s):\n  r-tcp:443  works on: MCI\n  ssh:2222   works on: MCI',
  };
}

Future<void> loadFonts() async {
  for (final f in [
    ('Inter', ['Inter-Regular', 'Inter-Medium', 'Inter-SemiBold', 'Inter-Bold']),
    ('JetBrainsMono', ['JetBrainsMono-Regular']),
    ('Vazirmatn', ['Vazirmatn-NL-Regular', 'Vazirmatn-NL-Medium', 'Vazirmatn-NL-SemiBold', 'Vazirmatn-NL-Bold']),
  ]) {
    final l = FontLoader(f.$1);
    for (final n in f.$2) {
      final file = File('assets/fonts/$n.ttf');
      if (file.existsSync()) l.addFont(Future.value(ByteData.view(file.readAsBytesSync().buffer)));
    }
    await l.load();
  }
  final icons = File(
    '${Platform.environment['FLUTTER_ROOT'] ?? '/home/user/sdk/flutter'}/bin/cache/artifacts/material_fonts/MaterialIcons-Regular.otf',
  );
  if (icons.existsSync()) {
    await (FontLoader('MaterialIcons')..addFont(Future.value(ByteData.view(icons.readAsBytesSync().buffer)))).load();
  }
}

void main() {
  setUpAll(() async {
    FlutterSecureStorage.setMockInitialValues({});
    await loadFonts();
  });

  testWidgets('tune: choose protocols, measure, deploy', (t) async {
    t.view.physicalSize = const Size(1170, 2532);
    t.view.devicePixelRatio = 3;
    final core = FakeCore();
    final s = AppState(core);
    final srv = ServerEntry(id: 'a', name: 'Frankfurt', host: '203.0.113.7');
    final overview = {
      'entries': [
        {'id': 'r-tcp', 'kind': 'reality-tcp', 'port': 443},
        {'id': 'mtproto', 'kind': 'mtproto', 'port': 443},
      ],
      'dnsDomain': '',
    };
    X.p = Palette.of('dark', 'green', Brightness.dark);
    await t.pumpWidget(
      AppScope(
        state: s,
        child: MaterialApp(
          debugShowCheckedModeBanner: false,
          theme: appTheme(),
          home: Scaffold(
            backgroundColor: Colors.transparent,
            body: Backdrop(
              child: TunePage(server: srv, overview: overview),
            ),
          ),
        ),
      ),
    );
    await t.pump(const Duration(milliseconds: 300));
    if (Platform.environment['SHOTS'] != null) await expectLater(find.byType(TunePage), matchesGoldenFile('shots/tune-setup.png'));

    // MTProto is deployed: on, and kept; the DNS tunnel asks for its zone
    expect(find.text('Deployed now: kept unless you turn it off'), findsOneWidget);
    await t.tap(find.text('DNS tunnel'));
    await t.pump();
    expect(find.text('DNS tunnel zone'), findsOneWidget);
    await t.dragUntilVisible(find.text('Start tuning'), find.byType(Scrollable).first, const Offset(0, -300));
    await t.tap(find.text('Start tuning'));
    await t.pump();
    expect(find.text('The DNS tunnel needs its zone.'), findsOneWidget);
    await t.enterText(find.byType(TextField), 't.example.com');
    await t.tap(find.text('SSH tunnel'));
    await t.tap(find.text('AmneziaWG'));
    await t.pump();
    await t.tap(find.text('Start tuning'));
    await t.pump();
    final opts = core.calls['manage.tune.start']!['options'] as Map;
    expect((opts['protocols'] as List).toSet(), {'mtproto', 'dnstt', 'ssh', 'awg'});
    expect(opts['dnsDomain'], 't.example.com');
    expect(opts['depth'], 'smart');

    await t.pump(const Duration(seconds: 1)); // the poll finds it measured
    await t.pump(const Duration(milliseconds: 300));
    expect(find.text('2 OF 3 GOT THROUGH'), findsOneWidget);
    if (Platform.environment['SHOTS'] != null) await expectLater(find.byType(TunePage), matchesGoldenFile('shots/tune-measured.png'));
    await t.dragUntilVisible(find.text('Deploy'), find.byType(Scrollable).first, const Offset(0, -300));
    await t.tap(find.text('2'));
    await t.pump();
    await t.tap(find.text('Deploy'));
    await t.pump();
    expect(core.calls['manage.tune.deploy'], {'id': 't1', 'n': 2});
    await t.pump(const Duration(seconds: 1));
    await t.pump(const Duration(milliseconds: 300));
    expect(find.text('Tune again'), findsOneWidget);
    if (Platform.environment['SHOTS'] != null) await expectLater(find.byType(TunePage), matchesGoldenFile('shots/tune-done.png'));
    await t.pumpWidget(const SizedBox());
  });

  testWidgets('settings: the accent row lines up; the tab bar has one highlight', (t) async {
    t.view.physicalSize = const Size(1170, 2532);
    t.view.devicePixelRatio = 3;
    final s = AppState(FakeCore())..settings.onboarded = true;
    X.p = Palette.of('dark', 'green', Brightness.dark);
    await t.pumpWidget(XtuneApp(state: s));
    await t.pump(const Duration(milliseconds: 500));
    await t.tap(find.descendant(of: find.byType(FloatingTabBar), matching: find.text('Settings')));
    await t.pump();
    await t.pump(const Duration(milliseconds: 500)); // the slide starts on the first frame, ends here
    // the window is laid out again (as on returning to the app): the pill must not slide
    t.view.physicalSize = const Size(900, 2532);
    await t.pump(const Duration(milliseconds: 16));
    t.view.physicalSize = const Size(1170, 2532);
    await t.pump(const Duration(milliseconds: 16));
    final bar = t.getRect(find.byType(FloatingTabBar));
    final pill = t.getRect(find.descendant(of: find.byType(FloatingTabBar), matching: find.byType(FractionallySizedBox)).last);
    expect(pill.center.dx, closeTo(bar.left + 22 + (bar.width - 44) * 0.9, 2), reason: 'Settings is the fifth slot: the pill is there at once');
    await t.dragUntilVisible(find.text('Accent'), find.byType(Scrollable).first, const Offset(0, -200));
    await t.pump(const Duration(milliseconds: 300));
    final accent = t.getRect(find.text('Accent'));
    final lang = t.getRect(find.text('Language'));
    expect(accent.left, lang.left, reason: 'the accent label starts where the rows below it do');
    if (Platform.environment['SHOTS'] != null) await expectLater(find.byType(MaterialApp), matchesGoldenFile('shots/settings.png'));
    await t.pumpWidget(const SizedBox());
  });
}
