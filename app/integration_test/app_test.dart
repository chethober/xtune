// Drives the real app and the real core on a desktop: add a subscription, connect in proxy mode,
// send traffic through it, run the on-device check. Needs a subscription server:
//
//   (cd core && go run ./cmd/devsub) &
//   XTUNE_CORE=/path/to/xtune-core XTUNE_SHOTS=/tmp/shots flutter test integration_test -d linux
import 'dart:io';
import 'dart:ui' as ui;

import 'package:flutter/rendering.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:integration_test/integration_test.dart';
import 'package:material_ui/material_ui.dart';
import 'package:xtune_client/main.dart' as app;

const sub = 'http://127.0.0.1:18080/p/tok';

Future<void> shot(WidgetTester tester, String name) async {
  final dir = Platform.environment['XTUNE_SHOTS'];
  if (dir == null) return;
  await settle(tester);
  final boundary = tester.renderObject<RenderRepaintBoundary>(find.byType(RepaintBoundary).first);
  await tester.runAsync(() async {
    final img = await boundary.toImage(pixelRatio: 2);
    final png = await img.toByteData(format: ui.ImageByteFormat.png);
    await File('$dir/$name.png').writeAsBytes(png!.buffer.asUint8List());
  });
}

/// Lets animations run for a while (the connected glow never settles, so pumpAndSettle would wait forever).
Future<void> settle(WidgetTester tester, [int ms = 700]) async {
  for (var i = 0; i < ms ~/ 50; i++) {
    await tester.pump(const Duration(milliseconds: 50));
  }
}

/// Scrolls [f] into view and clear of the floating tab bar, so a tap lands on it.
Future<void> reveal(WidgetTester tester, Finder f) async {
  if (f.evaluate().isEmpty) {
    // lists build their rows lazily: scroll the visible one until it appears
    await tester.dragUntilVisible(f, find.byType(Scrollable).hitTestable().first, const Offset(0, -250));
  }
  await tester.ensureVisible(f.first);
  await settle(tester, 300);
  final y = tester.getCenter(f.first).dy;
  final h = tester.view.physicalSize.height / tester.view.devicePixelRatio;
  if (y > h - 180) {
    await tester.drag(f.first, Offset(0, -(y - h / 2)));
    await settle(tester, 500);
  }
}

Future<void> waitFor(WidgetTester tester, Finder f, {int seconds = 30}) async {
  for (var i = 0; i < seconds * 5; i++) {
    await tester.pump(const Duration(milliseconds: 200));
    if (f.evaluate().isNotEmpty) return;
  }
  fail('timed out waiting for $f');
}

Future<String> getThroughProxy(WidgetTester tester) async {
  return (await tester.runAsync(() async {
    final c = HttpClient()..findProxy = (_) => 'PROXY 127.0.0.1:10809';
    try {
      final req = await c.getUrl(Uri.parse('http://probe.test/'));
      final resp = await req.close();
      return await resp.transform(const SystemEncoding().decoder).join();
    } catch (e) {
      return 'error: $e';
    } finally {
      c.close(force: true);
    }
  }))!;
}

void main() {
  IntegrationTestWidgetsFlutterBinding.ensureInitialized();

  testWidgets('add, connect, browse, check', (tester) async {
    final phone = Platform.environment['XTUNE_PHONE'] == '1';
    if (phone) {
      tester.view.physicalSize = const Size(390 * 2, 844 * 2);
      tester.view.devicePixelRatio = 2;
    }
    app.main();
    await waitFor(tester, find.text('Add subscription'));
    await shot(tester, '01-empty');

    await tester.tap(find.text('Add subscription').first);
    await settle(tester);
    await tester.enterText(find.byType(TextField).first, sub);
    await tester.tap(find.widgetWithText(FilledButton, 'Add'));
    await waitFor(tester, find.text('DE · dev'));

    // proxy mode: whole-device mode would ask for administrator rights
    await tester.tap(find.text('Settings').first);
    await settle(tester);
    await tester.tap(find.text('Proxy only'));
    await settle(tester);
    await shot(tester, '05-settings');

    await tester.tap(find.text('Home').first);
    await settle(tester);
    await tester.tap(find.bySemanticsLabel('Connect'));
    await waitFor(tester, find.text('Connected'));

    var body = '';
    for (var i = 0; i < 20 && body != 'through the proxy\n'; i++) {
      body = await getThroughProxy(tester);
      await tester.pump(const Duration(milliseconds: 500));
    }
    expect(body, 'through the proxy\n');
    await tester.pump(const Duration(seconds: 3)); // health checks, speed
    await shot(tester, '02-home-connected');

    await tester.tap(find.text('Configs').first);
    await settle(tester);
    expect(find.text('REALITY · XHTTP'), findsOneWidget);
    expect(find.text('Telegram proxy'), findsOneWidget);
    await shot(tester, '03-configs');

    await tester.tap(find.text('Check').first);
    await settle(tester);
    await tester.tap(find.text('Check now'));
    await waitFor(tester, find.textContaining('configs tested'), seconds: 60);
    expect(find.text('BLOCKED'), findsNWidgets(2)); // r-tcp points nowhere; nothing answers on the MTProto port
    expect(find.text('WORKS'), findsNWidgets(2));
    await shot(tester, '04-check');

    await tester.tap(find.text('Manage').first);
    await settle(tester);
    await shot(tester, '06-manage-empty');

    // 1.1: a single config's speed test, from its sheet
    await tester.tap(find.text('Configs').first);
    await settle(tester);
    await tester.longPress(find.text('Hysteria2').first);
    await settle(tester);
    await tester.tap(find.widgetWithText(FilledButton, 'Test'));
    await waitFor(tester, find.text('testing…'), seconds: 5);
    for (var i = 0; i < 200 && find.text('testing…').evaluate().isNotEmpty; i++) {
      await tester.pump(const Duration(milliseconds: 200));
    }
    expect(find.text('testing…'), findsNothing);
    expect(find.text('No answer: blocked here'), findsNothing); // Hysteria2 here is a working VLESS server
    await shot(tester, '07-config-sheet');
    await tester.tapAt(const Offset(10, 10)); // close the sheet
    await settle(tester);

    // routing rules from a preset
    await tester.tap(find.text('Settings').first);
    await settle(tester);
    await reveal(tester, find.text('Routing rules'));
    await tester.tap(find.text('Routing rules'));
    await settle(tester);
    await tester.tap(find.byTooltip('Presets'));
    await settle(tester);
    await tester.tap(find.text('Iran: domestic sites direct'));
    await settle(tester, 1500);
    expect(find.text('category-ir'), findsOneWidget);
    await shot(tester, '08-rules');
    await tester.tap(find.byTooltip('Back'));
    await settle(tester);

    // the usage page and the log
    await reveal(tester, find.text('Usage'));
    await tester.tap(find.text('Usage').first);
    await settle(tester, 1200);
    expect(find.text('BY CONFIG'), findsOneWidget);
    await shot(tester, '09-usage');
    await tester.tap(find.byTooltip('Back'));
    await settle(tester);
    await reveal(tester, find.text('Connection log'));
    await tester.tap(find.text('Connection log'));
    await settle(tester, 1500);
    expect(find.text('Nothing logged yet: connect first'), findsNothing);
    await tester.tap(find.byTooltip('Back'));
    await settle(tester);

    // the light theme: back to the top of Settings
    await tester.drag(find.text('Connection log'), const Offset(0, 4000));
    await settle(tester, 800);
    await reveal(tester, find.text('Light'));
    await tester.tap(find.text('Light'));
    await settle(tester);
    await shot(tester, '10-settings-light');
    await tester.tap(find.text('Home').first);
    await settle(tester, 1000);
    await shot(tester, '11-home-light');
    await tester.tap(find.text('Settings').first);
    await settle(tester);
    await reveal(tester, find.text('Dark'));
    await tester.tap(find.text('Dark'));
    await settle(tester);

    await tester.tap(find.text('Home').first);
    await settle(tester);
    await tester.tap(find.bySemanticsLabel('Disconnect'));
    await waitFor(tester, find.text('Not connected'));
  });
}
