import 'package:flutter_secure_storage/flutter_secure_storage.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:material_ui/material_ui.dart';
import 'package:xtune_client/core/core_client.dart';
import 'package:xtune_client/main.dart';
import 'package:xtune_client/screens/user_tune.dart';
import 'package:xtune_client/state/app_state.dart';
import 'package:xtune_client/state/models.dart';
import 'package:xtune_client/ui/glass.dart';

Map<String, dynamic> _view({bool selfTune = true, List<String> lock = const [], List<String> deny = const [], List<String> hide = const []}) => {
  'name': 'ann',
  'policy': {'deny': deny, 'selfTune': selfTune, 'lock': lock},
  'effective': {'deny': deny, 'selfTune': selfTune, 'lock': lock},
  'prefs': {'hide': hide, 'pin': []},
  'configs': [
    {'id': 'r-tcp', 'kind': 'reality-tcp', 'label': 'REALITY', 'allowed': !deny.contains('r-tcp'), 'listed': true, 'order': 1, 'knobs': 'fp=chrome'},
    {'id': 'ss2022', 'kind': 'ss2022', 'label': 'Shadowsocks', 'allowed': true, 'listed': !hide.contains('ss2022'), 'order': 2},
  ],
  'presets': ['mci'],
};

/// A core with a subscription server and a managed server that answer for one user's tuning.
class FakeCore extends CoreClient {
  final calls = <String, List<Map<String, dynamic>>>{};
  Map<String, dynamic> prefs = _view();
  Map<String, dynamic> admin = _view(selfTune: false);
  int polls = 0;
  @override
  Future<dynamic> call(String method, [Map<String, dynamic> args = const {}]) async {
    calls.putIfAbsent(method, () => []).add(args);
    switch (method) {
      case 'prefs.get':
        return prefs;
      case 'prefs.set':
        final p = args['prefs'] as Map;
        prefs = _view(hide: (p['hide'] as List).cast<String>());
        return prefs;
      case 'network.current':
        return {'asn': 'AS44244', 'name': 'Irancell'};
      case 'personal.fetch':
        return {
          'id': 'personal-ann',
          'profile': {
            'v': 1,
            'configs': [
              {'id': 'r-tcp#0', 'key': 'r-tcp|fp=chrome'},
            ],
          },
        };
      case 'probe.start':
        return {'id': '7', 'done': false, 'results': []};
      case 'probe.status':
        polls++;
        return {
          'id': '7',
          'done': polls > 1,
          'results': [
            {'id': 'r-tcp#0', 'key': 'r-tcp|fp=chrome', 'state': polls > 1 ? 'ok' : 'testing', 'ok': 3, 'n': 3},
          ],
        };
      case 'personal.send':
        return {
          'measured': 1,
          'chosen': {'r-tcp': 'fp=safari frag=frag-aggr'},
        };
      case 'profile.fetch':
        return {'name': 'sub', 'configs': []};
      case 'manage.userTune':
        return admin;
      case 'manage.setUserTune':
        final c = args['change'] as Map;
        admin = _view(selfTune: c['selfTune'] as bool? ?? false, deny: ((c['deny'] as List?) ?? []).cast<String>());
        return admin;
    }
    return null;
  }
}

Widget _host(AppState s, Widget child) => AppScope(
  state: s,
  child: MaterialApp(
    theme: appTheme(),
    home: Scaffold(body: SingleChildScrollView(child: child)),
  ),
);

void main() {
  setUpAll(() => FlutterSecureStorage.setMockInitialValues({}));

  testWidgets('check tab: the user hides a config and tunes their own', (t) async {
    t.view.physicalSize = const Size(1170, 4000);
    t.view.devicePixelRatio = 3;
    final core = FakeCore();
    final s = AppState(core);
    s.subs.add(Sub(id: 's1', url: 'https://sub.example.com/p/tok', profile: {'name': 'sub', 'configs': []}, fetched: DateTime.now()));
    s.activeSubId = 's1';
    X.p = Palette.of('dark', 'green', Brightness.dark);
    await t.pumpWidget(_host(s, const MyConfigsPanel()));
    await t.pump();
    await t.pump();
    expect(find.text('My configs'), findsOneWidget);
    expect(core.calls['prefs.get']!.single['url'], 'https://sub.example.com/p/tok');

    await t.tap(find.text('Edit'));
    await t.pump();
    await t.tap(find.byKey(const ValueKey('show-ss2022'))); // ss2022: hide it
    await t.pump();
    await t.pump();
    final set = core.calls['prefs.set']!.single['prefs'] as Map;
    expect(set['hide'], ['ss2022']);

    await t.tap(find.byKey(const ValueKey('personal-tune')));
    for (var i = 0; i < 6; i++) {
      await t.pump(const Duration(milliseconds: 800));
    }
    expect(core.calls['personal.send']!.single['results'], isNotEmpty);
    expect(find.textContaining('fp=safari frag=frag-aggr'), findsOneWidget);
    await t.pumpWidget(const SizedBox());
  });

  testWidgets('check tab: nothing for subscriptions that are not xtune servers', (t) async {
    final core = FakeCore();
    final s = AppState(core);
    s.subs.add(Sub(id: 's1', url: '', profile: {'name': 'pasted', 'configs': []}, fetched: DateTime.now()));
    s.activeSubId = 's1';
    X.p = Palette.of('dark', 'green', Brightness.dark);
    await t.pumpWidget(_host(s, const MyConfigsPanel()));
    await t.pump();
    expect(find.text('My configs'), findsNothing);
    expect(core.calls['prefs.get'], isNull);
  });

  testWidgets('manage tab: the admin turns self-tuning on and a config off', (t) async {
    t.view.physicalSize = const Size(1170, 4000);
    t.view.devicePixelRatio = 3;
    final core = FakeCore();
    final s = AppState(core);
    final srv = ServerEntry(id: 'a', name: 'Frankfurt', host: '203.0.113.7');
    X.p = Palette.of('dark', 'green', Brightness.dark);
    await t.pumpWidget(
      AppScope(
        state: s,
        child: MaterialApp(
          theme: appTheme(),
          home: Scaffold(
            body: Backdrop(
              child: UserTunePage(server: srv, name: 'ann'),
            ),
          ),
        ),
      ),
    );
    await t.pump();
    await t.pump();
    expect(find.text('Tuning: ann'), findsOneWidget);
    await t.tap(find.text('Can tune their own configs'));
    await t.pump();
    await t.pump();
    expect((core.calls['manage.setUserTune']!.last['change'] as Map)['selfTune'], true);
    await t.tap(find.byKey(const ValueKey('allow-r-tcp'))); // r-tcp off: denied on the server
    await t.pump();
    await t.pump();
    expect((core.calls['manage.setUserTune']!.last['change'] as Map)['deny'], ['r-tcp']);
    expect(core.calls['manage.setUserTune']!.last['name'], 'ann');
    await t.pumpWidget(const SizedBox());
  });
}
