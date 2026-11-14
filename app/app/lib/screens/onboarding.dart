import 'dart:io';

import 'package:material_ui/material_ui.dart';

import '../main.dart';
import '../ui/glass.dart';
import '../ui/i18n.dart';
import 'add_sub.dart';
import 'tools.dart';

/// The first run: what xtune is, a subscription (pasted or scanned), the VPN permission, done.
/// Shown once, when there is no subscription yet.
class Onboarding extends StatefulWidget {
  const Onboarding({super.key});
  @override
  State<Onboarding> createState() => _OnboardingState();
}

class _OnboardingState extends State<Onboarding> {
  int step = 0;

  Future<void> _finish() async {
    await AppScope.of(context).updateSettings((x) => x.onboarded = true);
  }

  @override
  Widget build(BuildContext context) {
    final s = AppScope.of(context);
    // a subscription added (or a backup restored) moves on by itself
    final current = step == 1 && s.sub != null ? 2 : step;
    final pages = [_welcome(context), _addSub(context), _permission(context)];
    return Scaffold(
      backgroundColor: Colors.transparent,
      body: Backdrop(
        child: SafeArea(
          child: Center(
            child: ConstrainedBox(
              constraints: const BoxConstraints(maxWidth: 460),
              child: Padding(
                padding: const EdgeInsets.all(Space.xl),
                child: Column(
                  children: [
                    Row(
                      children: [
                        for (var i = 0; i < 3; i++)
                          Expanded(
                            child: AnimatedContainer(
                              duration: Motion.normal,
                              height: 4,
                              margin: const EdgeInsets.symmetric(horizontal: 3),
                              decoration: BoxDecoration(color: i <= current ? X.accent : X.track, borderRadius: BorderRadius.circular(2)),
                            ),
                          ),
                      ],
                    ),
                    Align(
                      alignment: AlignmentDirectional.centerEnd,
                      child: TextButton(onPressed: _finish, child: Text(T.s('skip'))),
                    ),
                    Expanded(
                      child: AnimatedSwitcher(
                        duration: Motion.normal,
                        child: KeyedSubtree(
                          key: ValueKey(current),
                          child: Center(child: SingleChildScrollView(child: pages[current])),
                        ),
                      ),
                    ),
                  ],
                ),
              ),
            ),
          ),
        ),
      ),
    );
  }

  Widget _hero(IconData icon, String title, String body) => Column(
    children: [
      Container(
        width: 96,
        height: 96,
        decoration: BoxDecoration(
          shape: BoxShape.circle,
          color: X.accent.withValues(alpha: 0.14),
          border: Border.all(color: X.accent.withValues(alpha: 0.4)),
        ),
        child: Icon(icon, size: 44, color: X.accent),
      ),
      const SizedBox(height: Space.xl),
      Text(title, style: Type.title.copyWith(fontSize: 26), textAlign: TextAlign.center),
      const SizedBox(height: Space.m),
      Text(body, style: Type.secondary.copyWith(fontSize: 15), textAlign: TextAlign.center),
    ],
  );

  Widget _wide(String label, VoidCallback? onTap, {IconData? icon, bool primary = true}) => Padding(
    padding: const EdgeInsets.only(top: Space.m),
    child: primary
        ? FilledButton.icon(
            style: FilledButton.styleFrom(minimumSize: const Size.fromHeight(52)),
            onPressed: onTap,
            icon: Icon(icon ?? Icons.arrow_forward_rounded),
            label: Text(label),
          )
        : OutlinedButton.icon(
            style: OutlinedButton.styleFrom(minimumSize: const Size.fromHeight(52)),
            onPressed: onTap,
            icon: Icon(icon ?? Icons.arrow_forward_rounded),
            label: Text(label),
          ),
  );

  Widget _welcome(BuildContext context) => Column(
    children: [
      _hero(Icons.shield_rounded, T.s('obWelcome'), T.s('obWelcomeBody')),
      const SizedBox(height: Space.xl),
      _wide(T.s('obStart'), () => setState(() => step = 1)),
      _wide(T.s('backupRestore'), () => importBackup(context), icon: Icons.restore_rounded, primary: false),
    ],
  );

  Widget _addSub(BuildContext context) => Column(
    children: [
      _hero(Icons.link_rounded, T.s('obSub'), T.s('obSubBody')),
      const SizedBox(height: Space.xl),
      if (Platform.isAndroid) _wide(T.s('scanQr'), () => showAddSub(context, scan: true), icon: Icons.qr_code_scanner_rounded),
      _wide(T.s('obPaste'), () => showAddSub(context), icon: Icons.content_paste_rounded, primary: !Platform.isAndroid),
    ],
  );

  Widget _permission(BuildContext context) {
    final s = AppScope.of(context);
    return Column(
      children: [
        _hero(Icons.vpn_key_rounded, T.s('obReady'), Platform.isAndroid ? T.s('obReadyAndroid') : T.s('obReadyDesktop')),
        const SizedBox(height: Space.xl),
        _wide(T.s('connect'), () async {
          await _finish();
          await s.connect();
        }, icon: Icons.power_settings_new_rounded),
        _wide(T.s('obLater'), _finish, icon: Icons.check_rounded, primary: false),
      ],
    );
  }
}
