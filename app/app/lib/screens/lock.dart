import 'dart:async';
import 'dart:convert';
import 'dart:io';
import 'dart:math';

import 'package:crypto/crypto.dart';
import 'package:flutter/services.dart';
import 'package:local_auth/local_auth.dart';
import 'package:material_ui/material_ui.dart';

import '../main.dart';
import '../state/store.dart';
import '../ui/fmt.dart';
import '../ui/glass.dart';
import '../ui/i18n.dart';

/// The app lock: a PIN, or the fingerprint or face where the device has one, before the app (or
/// only the Manage tab, which holds servers' root logins) opens. It locks again after the app has
/// been away for [relockAfter].
class Lock {
  static const relockAfter = Duration(minutes: 1);
  static const _pinKey = 'lock-pin';
  static bool unlocked = false;
  static DateTime? _away;
  static int _fails = 0;
  static DateTime? _waitUntil;
  static final changes = ValueNotifier<int>(0);

  static String _hash(String pin, String salt) {
    List<int> h = utf8.encode('$salt:$pin');
    for (var i = 0; i < 20000; i++) {
      h = sha256.convert(h).bytes;
    }
    return base64.encode(h);
  }

  static Future<bool> hasPin() async => (await Secrets.read(_pinKey))?.isNotEmpty == true;

  static Future<void> setPin(String pin) async {
    final r = Random.secure();
    final salt = base64.encode(List.generate(12, (_) => r.nextInt(256)));
    await Secrets.write(_pinKey, '$salt:${_hash(pin, salt)}');
  }

  static Future<void> clearPin() => Secrets.delete(_pinKey);

  /// Seconds left before another try, after five wrong PINs.
  static int get waitSeconds {
    final w = _waitUntil;
    if (w == null) return 0;
    return max(0, w.difference(DateTime.now()).inSeconds);
  }

  static Future<bool> check(String pin) async {
    if (waitSeconds > 0) return false;
    final saved = await Secrets.read(_pinKey);
    if (saved == null) return true;
    final i = saved.indexOf(':');
    final ok = i > 0 && _hash(pin, saved.substring(0, i)) == saved.substring(i + 1);
    if (ok) {
      _fails = 0;
      unlock();
    } else if (++_fails >= 5) {
      _fails = 0;
      _waitUntil = DateTime.now().add(const Duration(seconds: 30));
    }
    return ok;
  }

  static void unlock() {
    unlocked = true;
    changes.value++;
  }

  /// The app went to the background, or came back.
  static void lifecycle(AppLifecycleState s) {
    if (s == AppLifecycleState.paused || s == AppLifecycleState.hidden) {
      _away ??= DateTime.now();
    } else if (s == AppLifecycleState.resumed) {
      final away = _away;
      _away = null;
      if (away != null && DateTime.now().difference(away) > relockAfter && unlocked) {
        unlocked = false;
        changes.value++;
      }
    }
  }

  static final _auth = LocalAuthentication();

  /// Whether the fingerprint or face can unlock (Android, Windows Hello).
  static Future<bool> biometricsAvailable() async {
    if (!Platform.isAndroid && !Platform.isWindows) return false;
    try {
      return await _auth.isDeviceSupported() && (Platform.isWindows || await _auth.canCheckBiometrics);
    } catch (_) {
      return false;
    }
  }

  static Future<bool> biometric() async {
    try {
      final ok = await _auth.authenticate(localizedReason: T.s('lockReason'), biometricOnly: Platform.isAndroid);
      if (ok) unlock();
      return ok;
    } on PlatformException catch (_) {
      return false;
    } catch (_) {
      return false;
    }
  }
}

/// Covers [child] with the lock screen while the lock is on and not yet opened. [scope] is what it
/// guards: "app" covers everything, "manage" only the Manage tab.
class LockGate extends StatelessWidget {
  const LockGate({super.key, required this.scope, required this.child});
  final String scope;
  final Widget child;

  @override
  Widget build(BuildContext context) {
    final s = AppScope.of(context);
    return ValueListenableBuilder<int>(
      valueListenable: Lock.changes,
      builder: (context, _, _) {
        final on = s.settings.lock == scope && !Lock.unlocked;
        if (!on) return child;
        return LockScreen(bio: s.settings.lockBio, compact: scope == 'manage');
      },
    );
  }
}

class LockScreen extends StatefulWidget {
  const LockScreen({super.key, required this.bio, this.compact = false});
  final bool bio;
  final bool compact;
  @override
  State<LockScreen> createState() => _LockScreenState();
}

class _LockScreenState extends State<LockScreen> {
  String pin = '';
  String? error;
  bool checking = false;
  Timer? tick;

  @override
  void initState() {
    super.initState();
    if (widget.bio) WidgetsBinding.instance.addPostFrameCallback((_) => Lock.biometric());
    tick = Timer.periodic(const Duration(seconds: 1), (_) {
      if (Lock.waitSeconds > 0 && mounted) setState(() {});
    });
  }

  @override
  void dispose() {
    tick?.cancel();
    super.dispose();
  }

  Future<void> _press(String d) async {
    if (checking || Lock.waitSeconds > 0) return;
    HapticFeedback.selectionClick();
    setState(() {
      error = null;
      if (d == '<') {
        if (pin.isNotEmpty) pin = pin.substring(0, pin.length - 1);
      } else if (pin.length < 8) {
        pin += d;
      }
    });
  }

  Future<void> _submit() async {
    if (pin.length < 4) return;
    setState(() => checking = true);
    final ok = await Lock.check(pin);
    if (!mounted) return;
    setState(() {
      checking = false;
      if (!ok) {
        error = Lock.waitSeconds > 0 ? T.s('lockWait', {'n': Lock.waitSeconds}) : T.s('lockWrong');
        pin = '';
        HapticFeedback.heavyImpact();
      }
    });
  }

  @override
  Widget build(BuildContext context) {
    final wait = Lock.waitSeconds;
    final pad = Column(
      mainAxisSize: MainAxisSize.min,
      children: [
        Icon(Icons.lock_rounded, size: 40, color: X.accent),
        const SizedBox(height: Space.m),
        Text(widget.compact ? T.s('lockManageTitle') : T.s('lockTitle'), style: Type.title, textAlign: TextAlign.center),
        const SizedBox(height: Space.s),
        Text(T.s('lockHint'), style: Type.secondary, textAlign: TextAlign.center),
        const SizedBox(height: Space.xl),
        Semantics(
          label: T.s('lockDigits', {'n': pin.length}),
          child: Row(
            mainAxisSize: MainAxisSize.min,
            children: [
              for (var i = 0; i < max(4, pin.length); i++)
                AnimatedContainer(
                  duration: Motion.fast,
                  margin: const EdgeInsets.symmetric(horizontal: 6),
                  width: 14,
                  height: 14,
                  decoration: BoxDecoration(
                    shape: BoxShape.circle,
                    color: i < pin.length ? X.accent : Colors.transparent,
                    border: Border.all(color: i < pin.length ? X.accent : X.muted, width: 1.5),
                  ),
                ),
            ],
          ),
        ),
        SizedBox(
          height: 36,
          child: Center(
            child: Text(wait > 0 ? T.s('lockWait', {'n': wait}) : (error ?? ''), style: Type.secondary.copyWith(color: X.bad)),
          ),
        ),
        Directionality(
          textDirection: TextDirection.ltr,
          child: SizedBox(
            width: 280,
            child: GridView.count(
              crossAxisCount: 3,
              shrinkWrap: true,
              physics: const NeverScrollableScrollPhysics(),
              mainAxisSpacing: Space.m,
              crossAxisSpacing: Space.m,
              childAspectRatio: 1.35,
              children: [
                for (final d in ['1', '2', '3', '4', '5', '6', '7', '8', '9']) _Key(label: nums(d), onTap: () => _press(d)),
                widget.bio ? _Key(icon: Icons.fingerprint_rounded, onTap: () => Lock.biometric(), label: T.s('lockBioShort')) : const SizedBox(),
                _Key(label: nums('0'), onTap: () => _press('0')),
                pin.length >= 4
                    ? _Key(icon: Icons.arrow_forward_rounded, onTap: _submit, label: T.s('lockOpen'), accent: true)
                    : _Key(icon: Icons.backspace_outlined, onTap: () => _press('<'), label: T.s('lockErase')),
              ],
            ),
          ),
        ),
      ],
    );
    if (widget.compact) {
      return XPage(
        title: T.s('manage'),
        children: [
          const SizedBox(height: Space.xl),
          Center(child: pad),
        ],
      );
    }
    return Scaffold(
      backgroundColor: Colors.transparent,
      body: Backdrop(
        child: SafeArea(
          child: Center(
            child: SingleChildScrollView(padding: const EdgeInsets.all(Space.xl), child: pad),
          ),
        ),
      ),
    );
  }
}

class _Key extends StatelessWidget {
  const _Key({this.label = '', this.icon, required this.onTap, this.accent = false});
  final String label;
  final IconData? icon;
  final VoidCallback onTap;
  final bool accent;

  @override
  Widget build(BuildContext context) => Semantics(
    button: true,
    label: label,
    excludeSemantics: true,
    child: Glass(
      radius: 40,
      padding: EdgeInsets.zero,
      color: accent ? X.accent : null,
      onTap: onTap,
      child: Center(
        child: icon != null
            ? Icon(icon, color: accent ? X.onAccent : X.text)
            : Text(label, style: Type.title.copyWith(fontSize: 24, fontWeight: FontWeight.w500)),
      ),
    ),
  );
}

/// Asks for a new PIN twice; null when cancelled.
Future<String?> askNewPin(BuildContext context) async {
  final a = TextEditingController(), b = TextEditingController();
  String? err;
  return showXSheet<String>(
    context,
    title: T.s('lockSetPin'),
    builder: (ctx, setState) => Column(
      mainAxisSize: MainAxisSize.min,
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Text(T.s('lockSetPinHint'), style: Type.secondary),
        const SizedBox(height: Space.l),
        TextField(
          controller: a,
          autofocus: true,
          obscureText: true,
          keyboardType: TextInputType.number,
          inputFormatters: [FilteringTextInputFormatter.digitsOnly, LengthLimitingTextInputFormatter(8)],
          decoration: InputDecoration(labelText: T.s('lockPin')),
        ),
        const SizedBox(height: Space.m),
        TextField(
          controller: b,
          obscureText: true,
          keyboardType: TextInputType.number,
          inputFormatters: [FilteringTextInputFormatter.digitsOnly, LengthLimitingTextInputFormatter(8)],
          decoration: InputDecoration(labelText: T.s('lockPinAgain'), errorText: err),
        ),
        const SizedBox(height: Space.l),
        Row(
          mainAxisAlignment: MainAxisAlignment.end,
          children: [
            TextButton(onPressed: () => Navigator.pop(ctx), child: Text(T.s('cancel'))),
            const SizedBox(width: Space.s),
            FilledButton(
              onPressed: () {
                if (a.text.length < 4) {
                  setState(() => err = T.s('lockPinShort'));
                } else if (a.text != b.text) {
                  setState(() => err = T.s('lockPinMismatch'));
                } else {
                  Navigator.pop(ctx, a.text);
                }
              },
              child: Text(T.s('save')),
            ),
          ],
        ),
      ],
    ),
  );
}
