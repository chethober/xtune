import 'package:material_ui/material_ui.dart';

/// The design tokens: every color, space, radius, text style and duration the app uses comes from
/// here, so screens share one look and one change restyles them all.

/// One look: the colors of a theme with an accent.
class Palette {
  const Palette({
    required this.brightness,
    required this.bg,
    required this.sheet,
    required this.text,
    required this.muted,
    required this.accent,
    required this.onAccent,
    required this.blue,
    required this.warn,
    required this.bad,
    required this.glass,
    required this.glassStrong,
    required this.edge,
    required this.track,
    required this.selected,
    required this.glowBlue,
    required this.glowGreen,
    required this.glowPurple,
    required this.surface,
    required this.menu,
  });
  final Brightness brightness;
  final Color bg, sheet, text, muted, accent, onAccent, blue, warn, bad, glass, glassStrong, edge, track, selected;
  final Color glowBlue, glowGreen, glowPurple, surface, menu;

  /// Accents, each with a darker shade that keeps 4.5:1 contrast on the light theme.
  static const accents = <String, (Color, Color)>{
    'green': (Color(0xFF3DDC97), Color(0xFF0E8A57)),
    'blue': (Color(0xFF6EA8FE), Color(0xFF1F5FCC)),
    'purple': (Color(0xFFB794F6), Color(0xFF7246C9)),
    'orange': (Color(0xFFF5B14C), Color(0xFFA85F00)),
  };

  static Palette dark(String accent) {
    final a = (accents[accent] ?? accents['green']!).$1;
    return Palette(
      brightness: Brightness.dark,
      bg: const Color(0xFF090C12),
      sheet: const Color(0xE6151B24),
      text: const Color(0xFFE6EDF3),
      muted: const Color(0xFF9BA7B4),
      accent: a,
      onAccent: const Color(0xFF090C12),
      blue: const Color(0xFF6EA8FE),
      warn: const Color(0xFFF5B14C),
      bad: const Color(0xFFFF7B72),
      glass: const Color(0x12FFFFFF),
      glassStrong: const Color(0x1AFFFFFF),
      edge: const Color(0x24FFFFFF),
      track: const Color(0x1AFFFFFF),
      selected: const Color(0x24FFFFFF),
      glowBlue: const Color(0xFF1E3A5F),
      glowGreen: const Color(0xFF0F4C3A),
      glowPurple: const Color(0xFF3B1F5C),
      surface: const Color(0xFF151B24),
      menu: const Color(0xFF1C2430),
    );
  }

  /// Pure black for OLED screens: the glows dimmed, panels a touch lighter to stay visible.
  static Palette black(String accent) {
    final d = dark(accent);
    return Palette(
      brightness: Brightness.dark,
      bg: const Color(0xFF000000),
      sheet: const Color(0xF20B0D11),
      text: d.text,
      muted: d.muted,
      accent: d.accent,
      onAccent: const Color(0xFF000000),
      blue: d.blue,
      warn: d.warn,
      bad: d.bad,
      glass: const Color(0x14FFFFFF),
      glassStrong: const Color(0x1CFFFFFF),
      edge: const Color(0x26FFFFFF),
      track: d.track,
      selected: d.selected,
      glowBlue: const Color(0xFF0A1626),
      glowGreen: const Color(0xFF06231B),
      glowPurple: const Color(0xFF180C26),
      surface: const Color(0xFF0B0D11),
      menu: const Color(0xFF14171D),
    );
  }

  static Palette light(String accent) {
    final a = (accents[accent] ?? accents['green']!).$2;
    return Palette(
      brightness: Brightness.light,
      bg: const Color(0xFFF2F4F8),
      sheet: const Color(0xF2FFFFFF),
      text: const Color(0xFF111826),
      muted: const Color(0xFF586273),
      accent: a,
      onAccent: const Color(0xFFFFFFFF),
      blue: const Color(0xFF1F5FCC),
      warn: const Color(0xFFA85F00),
      bad: const Color(0xFFC62E26),
      glass: const Color(0xB3FFFFFF),
      glassStrong: const Color(0xCCFFFFFF),
      edge: const Color(0x1A111826),
      track: const Color(0x14111826),
      selected: const Color(0x1A111826),
      glowBlue: const Color(0xFFC9DCF7),
      glowGreen: const Color(0xFFC8EEDD),
      glowPurple: const Color(0xFFE2D5F5),
      surface: const Color(0xFFFFFFFF),
      menu: const Color(0xFFFFFFFF),
    );
  }

  /// theme: system | dark | black | light.
  static Palette of(String theme, String accent, Brightness system) => switch (theme) {
    'light' => light(accent),
    'black' => black(accent),
    'dark' => dark(accent),
    _ => system == Brightness.light ? light(accent) : dark(accent),
  };
}

/// The current colors. Screens read them here; the app sets the palette from the settings.
class X {
  static Palette p = Palette.dark('green');
  static Color get bg => p.bg;
  static Color get sheet => p.sheet;
  static Color get text => p.text;
  static Color get muted => p.muted;
  static Color get accent => p.accent;
  static Color get onAccent => p.onAccent;
  static Color get blue => p.blue;
  static Color get warn => p.warn;
  static Color get bad => p.bad;
  static Color get glass => p.glass;
  static Color get glassStrong => p.glassStrong;
  static Color get edge => p.edge;
  static Color get track => p.track;
  static Color get selected => p.selected;
  static Color get glowBlue => p.glowBlue;
  static Color get glowGreen => p.glowGreen;
  static Color get glowPurple => p.glowPurple;
  static bool get isLight => p.brightness == Brightness.light;

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

/// The type scale (getters: the colors follow the theme).
class Type {
  static TextStyle get _base => TextStyle(fontFamily: Fonts.sans, fontFamilyFallback: Fonts.fallback, color: X.text);
  static TextStyle get display => _base.copyWith(fontSize: 26, fontWeight: FontWeight.w600, letterSpacing: -0.4, height: 1.2);
  static TextStyle get statusBig => _base.copyWith(fontSize: 26, fontWeight: FontWeight.w600, letterSpacing: -0.2);
  static TextStyle get title => _base.copyWith(fontSize: 20, fontWeight: FontWeight.w600, letterSpacing: -0.2);
  static TextStyle get wordmark => _base.copyWith(fontSize: 22, fontWeight: FontWeight.w700, letterSpacing: -0.6);
  static TextStyle get headline => _base.copyWith(fontSize: 16, fontWeight: FontWeight.w600);
  static TextStyle get body => _base.copyWith(fontSize: 15, fontWeight: FontWeight.w500);
  static TextStyle get bodyStrong => _base.copyWith(fontSize: 15, fontWeight: FontWeight.w600);
  static TextStyle get secondary => _base.copyWith(fontSize: 13, color: X.muted, height: 1.4);
  static TextStyle get caption => _base.copyWith(fontSize: 12, color: X.muted);
  static TextStyle get overline => _base.copyWith(fontSize: 12, color: X.muted, letterSpacing: 0.6);
  static TextStyle get tab => _base.copyWith(fontSize: 11, color: X.muted);
  static TextStyle get badge => _base.copyWith(fontSize: 10, fontWeight: FontWeight.w700, letterSpacing: 0.4);
  static TextStyle get stat => _base.copyWith(fontSize: 18, fontWeight: FontWeight.w600, fontFeatures: const [FontFeature.tabularFigures()]);
  static TextStyle get bigNumber => _base.copyWith(fontSize: 26, fontWeight: FontWeight.w600, fontFeatures: const [FontFeature.tabularFigures()]);
  static TextStyle get number => _base.copyWith(fontSize: 14, fontWeight: FontWeight.w600, fontFeatures: const [FontFeature.tabularFigures()]);
  static TextStyle get mono => TextStyle(fontFamily: Fonts.mono, fontSize: 12, height: 1.45, color: X.text);
}

ThemeData appTheme() {
  final p = X.p;
  final scheme = p.brightness == Brightness.light
      ? ColorScheme.light(primary: p.accent, onPrimary: p.onAccent, secondary: p.blue, surface: p.surface, onSurface: p.text, error: p.bad)
      : ColorScheme.dark(primary: p.accent, onPrimary: p.onAccent, secondary: p.blue, surface: p.surface, onSurface: p.text, error: p.bad);
  final base = ThemeData(
    brightness: p.brightness,
    scaffoldBackgroundColor: p.bg,
    fontFamily: Fonts.sans,
    fontFamilyFallback: Fonts.fallback,
    colorScheme: scheme,
    useMaterial3: true,
  );
  final field = OutlineInputBorder(
    borderRadius: BorderRadius.circular(Radii.m),
    borderSide: BorderSide(color: X.edge),
  );
  return base.copyWith(
    textTheme: base.textTheme.apply(bodyColor: X.text, displayColor: X.text, fontFamily: Fonts.sans),
    dialogTheme: DialogThemeData(
      backgroundColor: p.surface,
      shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(Radii.card)),
    ),
    inputDecorationTheme: InputDecorationTheme(
      filled: true,
      fillColor: X.glass,
      border: field,
      enabledBorder: field,
      focusedBorder: field.copyWith(borderSide: BorderSide(color: X.accent)),
      labelStyle: TextStyle(color: X.muted),
      hintStyle: TextStyle(color: X.muted),
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
        side: BorderSide(color: X.edge),
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
      checkColor: WidgetStatePropertyAll(X.onAccent),
      side: BorderSide(color: X.muted, width: 1.5),
    ),
    dividerTheme: DividerThemeData(color: X.edge, thickness: 1, space: 1),
    listTileTheme: ListTileThemeData(
      titleTextStyle: Type.body,
      subtitleTextStyle: Type.caption,
      iconColor: X.text,
      contentPadding: const EdgeInsets.symmetric(horizontal: Space.l),
    ),
    popupMenuTheme: PopupMenuThemeData(
      color: p.menu,
      shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(Radii.m)),
    ),
    snackBarTheme: SnackBarThemeData(
      behavior: SnackBarBehavior.floating,
      backgroundColor: p.menu,
      contentTextStyle: TextStyle(color: p.text),
    ),
    progressIndicatorTheme: ProgressIndicatorThemeData(color: p.accent),
  );
}
