import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:material_ui/material_ui.dart';
import 'package:xtune_client/ui/glass.dart';

import 'tune_page_test.dart' show loadFonts;

void main() {
  setUpAll(loadFonts);

  Future<void> pumpPage(WidgetTester t, Widget child, {String theme = 'dark'}) async {
    t.view.physicalSize = const Size(1170, 2532);
    t.view.devicePixelRatio = 3;
    X.p = Palette.of(theme, 'green', theme == 'light' ? Brightness.light : Brightness.dark);
    await t.pumpWidget(
      MaterialApp(
        debugShowCheckedModeBanner: false,
        theme: appTheme(),
        home: Scaffold(
          backgroundColor: Colors.transparent,
          body: Backdrop(child: child),
        ),
      ),
    );
  }

  testWidgets('a dropdown opens under its field, marks the value and picks another', (t) async {
    var value = 'chrome';
    await pumpPage(
      t,
      StatefulBuilder(
        builder: (context, setState) => XPage(
          title: 'Tweaks',
          children: [
            XDropdown<String>(
              label: 'Fingerprint',
              helper: 'The TLS fingerprint the configs show',
              value: value,
              items: const [
                XMenuItem('', 'As the server set it'),
                XMenuItem('chrome', 'chrome'),
                XMenuItem('firefox', 'firefox'),
                XMenuItem('safari', 'safari'),
              ],
              onChanged: (v) => setState(() => value = v),
            ),
            const SizedBox(height: 300),
          ],
        ),
      ),
    );
    final field = t.getRect(find.byType(InputDecorator));
    await t.tap(find.byType(InputDecorator));
    await t.pumpAndSettle();
    final menu = t.getRect(find.byType(SingleChildScrollView).last);
    expect(menu.top, greaterThan(field.bottom), reason: 'below the field');
    expect(menu.top - field.bottom, lessThan(20), reason: 'right below it, not below the helper text');
    expect(find.byIcon(Icons.check_rounded), findsOneWidget);
    if (Platform.environment['SHOTS'] != null) await expectLater(find.byType(MaterialApp), matchesGoldenFile('shots/dropdown.png'));
    await t.tap(find.text('firefox'));
    await t.pumpAndSettle();
    expect(value, 'firefox');
    expect(find.text('safari'), findsNothing, reason: 'closed');
    // dismissed: nothing changes
    await t.tap(find.byType(InputDecorator));
    await t.pumpAndSettle();
    await t.tapAt(const Offset(20, 800));
    await t.pumpAndSettle();
    expect(value, 'firefox');
  });

  testWidgets('a menu button keeps its menu on screen and closes on a choice', (t) async {
    String? picked;
    await pumpPage(
      t,
      XPage(
        title: 'Configs',
        actions: [
          XMenuButton<String>(
            tooltip: 'Sort',
            icon: Icons.sort_rounded,
            selected: 'best',
            onSelected: (v) => picked = v,
            items: const [
              XMenuItem('best', 'Best first'),
              XMenuItem('name', 'Name'),
              XMenuItem('latency', 'Latency'),
              XMenuItem('remove', 'Remove', icon: Icons.delete_outline_rounded, danger: true),
            ],
          ),
        ],
      ),
      theme: 'light',
    );
    await t.tap(find.byIcon(Icons.sort_rounded));
    await t.pumpAndSettle();
    final menu = t.getRect(find.byType(SingleChildScrollView).last);
    expect(menu.right, lessThanOrEqualTo(390 - 16 + 6.01), reason: 'inside the page margin');
    expect(menu.left, greaterThanOrEqualTo(16));
    if (Platform.environment['SHOTS'] != null) await expectLater(find.byType(MaterialApp), matchesGoldenFile('shots/menu-light.png'));
    await t.tap(find.text('Latency'));
    await t.pumpAndSettle();
    expect(picked, 'latency');
  });
}
