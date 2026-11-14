import 'dart:io';

import 'package:flutter/foundation.dart';
import 'package:flutter/services.dart';
import 'package:material_ui/material_ui.dart';

import 'core/core_client.dart';
import 'core/desktop_tray.dart';
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
  DesktopTray.init(state);
}

String? _look;

/// Sets the palette and the language from the settings. Screens read colors and strings from
/// statics (X, T), which no widget depends on, so on a change every element is rebuilt now,
/// not just the ones that happen to listen to the state (the rest would keep the old look until
/// something else rebuilt them, such as switching tabs).
void applyLook(AppState state, {bool rebuild = true}) {
  final st = state.settings;
  final system = WidgetsBinding.instance.platformDispatcher.platformBrightness;
  final look = '${st.theme}|${st.accent}|${st.lang}|$system';
  if (look == _look) return;
  final first = _look == null;
  _look = look;
  T.lang = st.lang;
  X.p = Palette.of(st.theme, st.accent, system);
  if (first || !rebuild) return;
  void mark(Element e) {
    e.markNeedsBuild();
    e.visitChildren(mark);
  }

  WidgetsBinding.instance.rootElement?.visitChildren(mark);
}

/// Gives the screens the app state.
class AppScope extends InheritedNotifier<AppState> {
  const AppScope({super.key, required AppState state, required super.child}) : super(notifier: state);
  static AppState of(BuildContext context) => context.dependOnInheritedWidgetOfExactType<AppScope>()!.notifier!;
}

class XtuneApp extends StatefulWidget {
  const XtuneApp({super.key, required this.state});
  final AppState state;

  @override
  State<XtuneApp> createState() => _XtuneAppState();
}

class _XtuneAppState extends State<XtuneApp> {
  // added before the builder below listens, so the new look is in place when it rebuilds
  void _changed() => applyLook(widget.state);

  @override
  void initState() {
    super.initState();
    applyLook(widget.state, rebuild: false);
    widget.state.addListener(_changed);
  }

  @override
  void dispose() {
    widget.state.removeListener(_changed);
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final state = widget.state;
    return AppScope(
      state: state,
      child: ListenableBuilder(
        listenable: state,
        builder: (context, _) {
          applyLook(state, rebuild: false);
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
    final tabs = [
      TabItem(Icons.shield_outlined, T.s('home')),
      TabItem(Icons.list_rounded, T.s('configs')),
      TabItem(Icons.monitor_heart_outlined, T.s('check')),
      TabItem(Icons.dns_outlined, T.s('manage')),
      TabItem(Icons.tune_rounded, T.s('settings')),
    ];
    // one tab bar at the bottom everywhere; each page lays itself out the same way (XPage): one
    // centred column, or on wide windows a column with a side pane beside it
    final body = IndexedStack(
      index: index,
      children: [
        HomeScreen(onOpenConfigs: () => go(1)),
        const ConfigsScreen(),
        const CheckScreen(),
        const LockGate(scope: 'manage', child: ManageScreen()),
        const SettingsScreen(),
      ],
    );
    return Scaffold(
      backgroundColor: Colors.transparent,
      extendBody: true,
      body: Backdrop(child: SafeArea(bottom: false, child: body)),
      bottomNavigationBar: SafeArea(
        top: false,
        child: Center(
          heightFactor: 1,
          child: ConstrainedBox(
            constraints: const BoxConstraints(maxWidth: 560),
            child: FloatingTabBar(items: tabs, index: index, onTap: go),
          ),
        ),
      ),
    );
  }
}
