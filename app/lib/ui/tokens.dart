import 'package:material_ui/material_ui.dart';

/// The design tokens: every color, space, radius, text style and duration the app uses comes from
/// here, so screens share one look and one change restyles them all.

/// Colors: a deep backdrop with soft color under frosted glass, one accent for "connected".
class X {
  static const bg = Color(0xFF090C12);
  static const sheet = Color(0xE6151B24); // bottom sheets and dialogs over glass
  static const text = Color(0xFFE6EDF3);
  static const muted = Color(0xFF9BA7B4);
  static const accent = Color(0xFF3DDC97);
  static const onAccent = bg;
  static const blue = Color(0xFF6EA8FE);
  static const warn = Color(0xFFF5B14C);
  static const bad = Color(0xFFFF7B72);
  static const glass = Color(0x12FFFFFF);
  static const glassStrong = Color(0x1AFFFFFF);
  static const edge = Color(0x24FFFFFF);
  static const track = Color(0x1AFFFFFF);
  static const selected = Color(0x24FFFFFF); // the pill under a selected tab or segment

  // backdrop glows
  static const glowBlue = Color(0xFF1E3A5F);
  static const glowGreen = Color(0xFF0F4C3A);
  static const glowPurple = Color(0xFF3B1F5C);

  /// A status color: ok, warn, bad, or neutral.
  static Color status(String s) => switch (s) {
        'ok' => accent,
        'slow' || 'throttled' || 'flaky' || 'warn' => warn,
        'blocked' || 'bad' => bad,
        'ext' => blue,
        _ => muted,
      };
}

/// Spacing, on a 4-point grid.
class Space {
  static const xs = 4.0;
  static const s = 8.0;
  static const m = 12.0;
  static const l = 16.0;
  static const xl = 20.0;
  static const xxl = 24.0;
  static const page = 20.0; // a page's side margin
  static const tabBar = 112.0; // room left under content for the floating tab bar
}

class Radii {
  static const s = 10.0;
  static const m = 14.0;
  static const l = 18.0;
  static const card = 22.0;
  static const bar = 32.0;
}

/// Durations and curves.
class Motion {
  static const fast = Duration(milliseconds: 150);
  static const normal = Duration(milliseconds: 240);
  static const slow = Duration(milliseconds: 420);
  static const curve = Curves.easeOutCubic;
  static const spring = Curves.elasticOut;
}

/// Font families: Inter for Latin, Vazirmatn for Persian (it holds only the Arabic script, so it
/// sits behind Inter and Latin stays in Inter), JetBrains Mono for consoles and logs.
class Fonts {
  static const sans = 'Inter';
  static const fallback = ['Vazirmatn'];
  static const mono = 'JetBrainsMono';
}

/// The type scale.
class Type {
  static const _base = TextStyle(fontFamily: Fonts.sans, fontFamilyFallback: Fonts.fallback, color: X.text);
  static final display = _base.copyWith(fontSize: 26, fontWeight: FontWeight.w600, letterSpacing: -0.4, height: 1.2);
  static final statusBig = _base.copyWith(fontSize: 26, fontWeight: FontWeight.w600, letterSpacing: -0.2);
  static final title = _base.copyWith(fontSize: 20, fontWeight: FontWeight.w600, letterSpacing: -0.2);
  static final wordmark = _base.copyWith(fontSize: 22, fontWeight: FontWeight.w700, letterSpacing: -0.6);
  static final headline = _base.copyWith(fontSize: 16, fontWeight: FontWeight.w600);
  static final body = _base.copyWith(fontSize: 15, fontWeight: FontWeight.w500);
  static final bodyStrong = _base.copyWith(fontSize: 15, fontWeight: FontWeight.w600);
  static final secondary = _base.copyWith(fontSize: 13, color: X.muted, height: 1.4);
  static final caption = _base.copyWith(fontSize: 12, color: X.muted);
  static final overline = _base.copyWith(fontSize: 12, color: X.muted, letterSpacing: 0.6);
  static final tab = _base.copyWith(fontSize: 11, color: X.muted);
  static final badge = _base.copyWith(fontSize: 10, fontWeight: FontWeight.w700, letterSpacing: 0.4);
  static final stat = _base.copyWith(fontSize: 18, fontWeight: FontWeight.w600, fontFeatures: const [FontFeature.tabularFigures()]);
  static final bigNumber = _base.copyWith(fontSize: 26, fontWeight: FontWeight.w600, fontFeatures: const [FontFeature.tabularFigures()]);
  static final number = _base.copyWith(fontSize: 14, fontWeight: FontWeight.w600, fontFeatures: const [FontFeature.tabularFigures()]);
  static final mono = const TextStyle(fontFamily: Fonts.mono, fontSize: 12, height: 1.45, color: X.text);
}

ThemeData appTheme() {
  final base = ThemeData(
    brightness: Brightness.dark,
    scaffoldBackgroundColor: X.bg,
    fontFamily: Fonts.sans,
    fontFamilyFallback: Fonts.fallback,
    colorScheme: const ColorScheme.dark(primary: X.accent, onPrimary: X.onAccent, secondary: X.blue, surface: Color(0xFF121821), error: X.bad),
    useMaterial3: true,
  );
  final field = OutlineInputBorder(borderRadius: BorderRadius.circular(Radii.m), borderSide: const BorderSide(color: X.edge));
  return base.copyWith(
    textTheme: base.textTheme.apply(bodyColor: X.text, displayColor: X.text, fontFamily: Fonts.sans),
    dialogTheme: DialogThemeData(backgroundColor: const Color(0xFF151B24), shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(Radii.card))),
    inputDecorationTheme: InputDecorationTheme(
      filled: true,
      fillColor: X.glass,
      border: field,
      enabledBorder: field,
      focusedBorder: field.copyWith(borderSide: const BorderSide(color: X.accent)),
      labelStyle: const TextStyle(color: X.muted),
      hintStyle: const TextStyle(color: X.muted),
    ),
    filledButtonTheme: FilledButtonThemeData(
      style: FilledButton.styleFrom(
        backgroundColor: X.accent,
        foregroundColor: X.onAccent,
        minimumSize: const Size(0, 48),
        textStyle: const TextStyle(fontFamily: Fonts.sans, fontWeight: FontWeight.w600, fontSize: 15),
        shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(Radii.m)),
      ),
    ),
    outlinedButtonTheme: OutlinedButtonThemeData(
      style: OutlinedButton.styleFrom(
        foregroundColor: X.text,
        backgroundColor: X.glass,
        side: const BorderSide(color: X.edge),
        minimumSize: const Size(0, 48),
        shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(Radii.m)),
      ),
    ),
    switchTheme: SwitchThemeData(
      trackColor: WidgetStateProperty.resolveWith((s) => s.contains(WidgetState.selected) ? X.accent : X.track),
      thumbColor: WidgetStateProperty.resolveWith((s) => s.contains(WidgetState.selected) ? X.onAccent : X.muted),
      trackOutlineColor: const WidgetStatePropertyAll(Colors.transparent),
    ),
    checkboxTheme: CheckboxThemeData(
      fillColor: WidgetStateProperty.resolveWith((s) => s.contains(WidgetState.selected) ? X.accent : Colors.transparent),
      checkColor: const WidgetStatePropertyAll(X.onAccent),
      side: const BorderSide(color: X.muted, width: 1.5),
    ),
    dividerTheme: const DividerThemeData(color: X.edge, thickness: 1, space: 1),
    listTileTheme: ListTileThemeData(
      titleTextStyle: Type.body,
      subtitleTextStyle: Type.caption,
      iconColor: X.text,
      contentPadding: const EdgeInsets.symmetric(horizontal: Space.l),
    ),
    popupMenuTheme: PopupMenuThemeData(color: const Color(0xFF1C2430), shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(Radii.m))),
    snackBarTheme: const SnackBarThemeData(behavior: SnackBarBehavior.floating, backgroundColor: Color(0xFF1C2430)),
    progressIndicatorTheme: const ProgressIndicatorThemeData(color: X.accent),
  );
}
