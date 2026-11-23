import 'package:flutter_secure_storage/flutter_secure_storage.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:material_ui/material_ui.dart';
import 'package:shared_preferences/shared_preferences.dart';
import 'package:xtune_client/main.dart';
import 'package:xtune_client/screens/options.dart';
import 'package:xtune_client/state/app_state.dart';
import 'package:xtune_client/state/models.dart';
import 'package:xtune_client/ui/glass.dart' show Bar;

import 'tune_page_test.dart' show loadFonts;
import 'v13_test.dart' show TableCore, phone, settle;

void main() {
  setUpAll(() async {
    FlutterSecureStorage.setMockInitialValues({});
    await loadFonts();
  });

  Future<(AppState, TableCore)> rulesPage(WidgetTester t, List<RouteRule> rules, Map<String, dynamic> Function() status) async {
    await phone(t);
    SharedPreferences.setMockInitialValues({});
    final core = TableCore({'geo.status': (_) => status(), 'geo.download': (_) => status(), 'geo.delete': (_) => null});
    final s = AppState(core)..settings.onboarded = true;
    s.settings.rules = rules;
    await t.runAsync(s.openPrefsForTest);
    await t.pumpWidget(XtuneApp(state: s));
    await settle(t);
    t.state<NavigatorState>(find.byType(Navigator).first).push(MaterialPageRoute<void>(builder: (_) => const RulesPage()));
    await settle(t);
    await t.runAsync(() => Future<void>.delayed(const Duration(milliseconds: 50)));
    await settle(t);
    return (s, core);
  }

  const builtin = ['private', 'ir', 'cn', 'ru'];

  testWidgets('country list: offered only when a rule needs a country that is not built in', (t) async {
    var st = <String, dynamic>{'builtin': builtin, 'full': false};
    final (_, core) = await rulesPage(t, [
      RouteRule(match: 'geoip', value: 'IR', action: 'direct'),
      RouteRule(match: 'geoip', value: 'us', action: 'proxy'),
      RouteRule(match: 'geoip', value: '!de', action: 'block'),
    ], () => st);
    expect(find.text('Country IP lists'), findsOneWidget);
    expect(find.textContaining('Rules for de, us need the full list'), findsOneWidget, reason: 'ir is built in');

    st = {'builtin': builtin, 'full': false, 'job': {'state': 'downloading', 'done': 12 << 20, 'total': 24 << 20}};
    await t.tap(find.text('Download the full list (24 MB)'));
    await t.runAsync(() => Future<void>.delayed(const Duration(milliseconds: 50)));
    await settle(t);
    expect(core.calls.map((c) => c.$1), contains('geo.download'));
    expect(find.byType(Bar), findsOneWidget);

    st = {'builtin': builtin, 'full': true, 'job': {'state': 'ready'}};
    await t.runAsync(() => Future<void>.delayed(const Duration(milliseconds: 700))); // the next poll
    await settle(t);
    expect(find.textContaining('rules can use any country'), findsOneWidget);
    expect(find.text('Delete it (24 MB)'), findsOneWidget);
    await t.pumpWidget(const SizedBox());
  });

  testWidgets('country list: hidden when every rule uses a built-in country', (t) async {
    await rulesPage(t, [RouteRule(match: 'geoip', value: 'ru', action: 'direct'), RouteRule(match: 'domain', value: 'x.example', action: 'proxy')],
        () => {'builtin': builtin, 'full': false});
    expect(find.text('Country IP lists'), findsNothing);
    await t.pumpWidget(const SizedBox());
  });
}
