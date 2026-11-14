import 'package:flutter_secure_storage/flutter_secure_storage.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:material_ui/material_ui.dart';
import 'package:shared_preferences/shared_preferences.dart';
import 'package:xtune_client/main.dart';
import 'package:xtune_client/state/app_state.dart';
import 'package:xtune_client/state/models.dart';
import 'package:xtune_client/ui/glass.dart';
import 'package:xtune_client/ui/route_map.dart';

import 'tune_page_test.dart' show loadFonts;
import 'v13_test.dart' show TableCore, phone, settle, shots;

Sub sub() => Sub(
  id: 's',
  url: 'https://sub.example/p/token',
  fetched: DateTime.now(),
  profile: {
    'name': 'Home',
    'country': 'ir',
    'configs': [
      {'id': 'a', 'name': '🇮🇷→🇳🇱 Reality', 'kind': 'reality-tcp', 'engine': 'xray', 'server': '198.51.100.1'},
      {'id': 'b', 'name': '🇩🇪 XHTTP', 'kind': 'cdn-xhttp', 'engine': 'xray', 'server': '203.0.113.7'},
      {'id': 'c', 'name': 'IR→FI ws', 'kind': 'ws', 'engine': 'xray', 'server': '198.51.100.2'},
      {'id': 'd', 'name': 'no flag here', 'kind': 'ws', 'engine': 'xray', 'server': '192.0.2.9'},
    ],
  },
);

void main() {
  setUpAll(() async {
    FlutterSecureStorage.setMockInitialValues({});
    await loadFonts();
  });

  test('countries from config names: flags, or codes around an arrow', () {
    expect(countriesIn('🇮🇷→🇳🇱 Reality'), ['IR', 'NL']);
    expect(countriesIn('🇩🇪 XHTTP'), ['DE']);
    expect(countriesIn('IR→FI ws'), ['IR', 'FI']);
    expect(countriesIn('IR->DE'), ['IR', 'DE']);
    expect(countriesIn('VPN TCP'), isEmpty);
    expect(countriesIn('🇽🇽 nowhere'), isEmpty, reason: 'not a country the map knows');
  });

  testWidgets('home: the route map shows each hop and its delay', (t) async {
    await phone(t);
    SharedPreferences.setMockInitialValues({});
    final core = TableCore({
      'net.rtt': (a) => {for (final h in a['hosts'] as List) h: h == '198.51.100.1' ? 18 : 64},
    });
    final s = AppState(core)
      ..settings.onboarded = true
      ..subs = [sub()]
      ..status = {
        'running': true,
        'active': 'a',
        'outbounds': [
          {'id': 'a', 'alive': true, 'delayMs': 120},
          {'id': 'b', 'alive': true, 'delayMs': 90},
          {'id': 'c', 'alive': false, 'delayMs': 0},
        ],
      };
    await t.runAsync(s.openPrefsForTest);
    await t.pumpWidget(XtuneApp(state: s));
    await settle(t);
    await t.runAsync(() => Future<void>.delayed(const Duration(milliseconds: 50)));
    await settle(t);
    expect(core.calls.where((c) => c.$1 == 'net.rtt'), isNotEmpty);
    expect(find.byType(RouteMap), findsOneWidget);
    // the route in use: 18 ms to the relay, the rest of its 120 ms beyond it
    expect(find.text('18 + ≈102 = 120 ms'), findsOneWidget);
    expect(find.text('64 + ≈26 = 90 ms'), findsOneWidget);
    expect(find.text('—'), findsWidgets, reason: 'a route that does not work');
    if (shots) await expectLater(find.byType(MaterialApp), matchesGoldenFile('shots/route-map.png'));
    // tapping a route picks its config
    await t.drag(find.byType(Scrollable).first, const Offset(0, -400));
    await settle(t);
    if (shots) await expectLater(find.byType(RouteMap), matchesGoldenFile('shots/route-map-card.png'));
    await t.tap(find.text('64 + ≈26 = 90 ms'));
    await t.pump();
    expect(s.settings.selected, 'b');
    s.status = {};
    await t.pumpWidget(const SizedBox());
  });

  testWidgets('a theme change repaints every screen at once', (t) async {
    await phone(t);
    SharedPreferences.setMockInitialValues({});
    final s = AppState(TableCore({}))
      ..settings.onboarded = true
      ..settings.theme = 'dark';
    await t.runAsync(s.openPrefsForTest);
    await t.pumpWidget(XtuneApp(state: s));
    await settle(t);
    await t.tap(find.text('Settings').last);
    await settle(t);
    // the backdrop behind the tabs: Shell builds it and listens to nothing, so before the fix it
    // kept the old color until a tab switch rebuilt it
    Color backdrop() =>
        (t.widget<DecoratedBox>(find.descendant(of: find.byType(Backdrop), matching: find.byType(DecoratedBox)).first).decoration as BoxDecoration)
            .color!;
    final before = backdrop();
    await t.tap(find.text('Light'));
    await t.runAsync(() => Future<void>.delayed(const Duration(milliseconds: 50)));
    await t.pump();
    expect(X.isLight, isTrue);
    expect(backdrop(), isNot(before));
    expect(backdrop(), X.bg);
    await t.pumpWidget(const SizedBox());
  });
}
