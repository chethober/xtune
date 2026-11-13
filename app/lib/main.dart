import 'dart:io';

import 'package:flutter/foundation.dart';
import 'package:flutter/services.dart';
import 'package:material_ui/material_ui.dart';

import 'core/core_client.dart';
import 'screens/add_sub.dart';
import 'screens/check.dart';
import 'screens/configs.dart';
import 'screens/home.dart';
import 'screens/lock.dart';
import 'screens/onboarding.dart';
import 'screens/manage.dart';
import 'screens/settings.dart';
import 'state/app_state.dart';
import 'ui/glass.dart';
import 'ui/i18n.dart';

void main() async {
  WidgetsFlutterBinding.ensureInitialized();
  LicenseRegistry.addLicense(() async* {
    for (final f in ['Inter', 'Vazirmatn', 'JetBrainsMono']) {
      yield LicenseEntryWithLineBreaks([f], await rootBundle.loadString('assets/fonts/$f-OFL.txt'));
    }
  });
  final state = AppState(CoreClient.instance);
  await state.init();
  runApp(XtuneApp(state: state));
}

/// Gives the screens the app state.
class AppScope extends InheritedNotifier<AppState> {
  const AppScope({super.key, required AppState state, required super.child}) : super(notifier: state);
  static AppState of(BuildContext context) => context.dependOnInheritedWidgetOfExactType<AppScope>()!.notifier!;
}

class XtuneApp extends StatelessWidget {
  const XtuneApp({super.key, required this.state});
  final AppState state;

  @override
  Widget build(BuildContext context) {
    return AppScope(
      state: state,
      child: ListenableBuilder(
        listenable: state,
        builder: (context, _) {
          T.lang = state.settings.lang;
          X.p = Palette.of(state.settings.theme, state.settings.accent, WidgetsBinding.instance.platformDispatcher.platformBrightness);
          return MaterialApp(
            title: 'xtune',
            debugShowCheckedModeBanner: false,
            theme: appTheme(),
            builder: (context, child) => Directionality(textDirection: T.rtl ? TextDirection.rtl : TextDirection.ltr, child: child!),
            // the first run shows the guide; the app lock, when on, covers everything
            home: !state.settings.onboarded && state.subs.isEmpty ? const Onboarding() : const LockGate(scope: 'app', child: Shell()),
          );
        },
      ),
    );
  }
}

/// The width a page with a second pane may take on wide screens.
const twoPaneWidth = 1240.0;

/// Whether a page has room for a second pane (the list and its details side by side).
bool twoPane(BuildContext context) => MediaQuery.sizeOf(context).width >= 1180;

class Shell extends StatefulWidget {
  const Shell({super.key});
  @override
  State<Shell> createState() => _ShellState();
}

class _ShellState extends State<Shell> with WidgetsBindingObserver {
  int index = 0;

  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addObserver(this);
    if (Platform.isAndroid) {
      AndroidCore.channel.setMethodCallHandler((call) async {
        if (call.method == 'link' && mounted) await _openLink(call.arguments as String?);
      });
      WidgetsBinding.instance.addPostFrameCallback((_) async {
        try {
          await _openLink(await AndroidCore.channel.invokeMethod<String>('initialLink'));
        } on PlatformException catch (_) {}
      });
    }
  }

  /// An xtune://import?url=… link: add the subscription it carries.
  Future<void> _openLink(String? link) async {
    if (link == null || !link.startsWith('xtune://') || !mounted) return;
    if (link.startsWith('xtune://manage')) {
      go(3); // the server widget
      return;
    }
    await showAddSub(context, initial: link);
  }

  @override
  void dispose() {
    WidgetsBinding.instance.removeObserver(this);
    super.dispose();
  }

  @override
  void didChangePlatformBrightness() => AppScope.of(context).themeChanged(); // "system" theme follows it

  @override
  void didChangeAppLifecycleState(AppLifecycleState s) {
    Lock.lifecycle(s);
    if (s == AppLifecycleState.detached && !Platform.isAndroid) {
      final core = CoreClient.instance;
      if (core is DesktopCore) core.shutdown();
    }
  }

  void go(int i) => setState(() => index = i);

  @override
  Widget build(BuildContext context) {
    final wide = MediaQuery.sizeOf(context).width >= 900;
    final tabs = [
      TabItem(Icons.shield_outlined, T.s('home')),
      TabItem(Icons.list_rounded, T.s('configs')),
      TabItem(Icons.monitor_heart_outlined, T.s('check')),
      TabItem(Icons.dns_outlined, T.s('manage')),
      TabItem(Icons.tune_rounded, T.s('settings')),
    ];
    final pages = [
      HomeScreen(onOpenConfigs: () => go(1)),
      const ConfigsScreen(),
      const CheckScreen(),
      const LockGate(scope: 'manage', child: ManageScreen()),
      const SettingsScreen(),
    ];
    final body = IndexedStack(index: index, children: pages);
    if (wide) {
      return Scaffold(
        backgroundColor: Colors.transparent,
        body: Backdrop(
          child: SafeArea(
            child: Row(
              children: [
                Padding(
                  padding: const EdgeInsets.all(16),
                  child: SizedBox(
                    width: 92,
                    child: Glass(
                      radius: 28,
                      padding: const EdgeInsets.symmetric(vertical: 12, horizontal: 6),
                      color: X.glassStrong,
                      child: Column(
                        children: [
                          Padding(
                            padding: const EdgeInsets.only(bottom: Space.l, top: Space.xs),
                            child: Text('xtune', style: Type.wordmark.copyWith(fontSize: 18)),
                          ),
                          for (var i = 0; i < tabs.length; i++)
                            Padding(
                              padding: const EdgeInsets.symmetric(vertical: 4),
                              child: InkWell(
                                borderRadius: BorderRadius.circular(20),
                                onTap: () => go(i),
                                child: Container(
                                  padding: const EdgeInsets.symmetric(vertical: 10),
                                  decoration: BoxDecoration(
                                    color: i == index ? Colors.white.withValues(alpha: 0.14) : Colors.transparent,
                                    borderRadius: BorderRadius.circular(20),
                                  ),
                                  child: Column(
                                    children: [
                                      Icon(tabs[i].icon, color: i == index ? X.accent : X.muted),
                                      const SizedBox(height: 4),
                                      Text(
                                        tabs[i].label,
                                        style: Type.tab.copyWith(color: i == index ? X.accent : X.muted),
                                        maxLines: 1,
                                        overflow: TextOverflow.ellipsis,
                                      ),
                                    ],
                                  ),
                                ),
                              ),
                            ),
                        ],
                      ),
                    ),
                  ),
                ),
                // one column of reading width; Configs and Manage get a second pane beside it
                Expanded(
                  child: IndexedStack(
                    index: index,
                    children: [
                      for (var i = 0; i < pages.length; i++)
                        Align(
                          alignment: Alignment.topCenter,
                          child: ConstrainedBox(
                            constraints: BoxConstraints(maxWidth: (i == 1 || i == 3) ? twoPaneWidth : 760),
                            child: pages[i],
                          ),
                        ),
                    ],
                  ),
                ),
              ],
            ),
          ),
        ),
      );
    }
    return Scaffold(
      backgroundColor: Colors.transparent,
      extendBody: true,
      body: Backdrop(child: SafeArea(bottom: false, child: body)),
      bottomNavigationBar: SafeArea(
        top: false,
        child: FloatingTabBar(items: tabs, index: index, onTap: go),
      ),
    );
  }
}
