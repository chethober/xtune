import 'dart:ui';

import 'package:material_ui/material_ui.dart';

/// The palette from the mockups: a deep backdrop with soft color, frosted panels, one accent.
class X {
  static const bg = Color(0xFF090C12);
  static const text = Color(0xFFE6EDF3);
  static const muted = Color(0xFF9BA7B4);
  static const accent = Color(0xFF3DDC97);
  static const blue = Color(0xFF6EA8FE);
  static const warn = Color(0xFFF5B14C);
  static const bad = Color(0xFFFF7B72);
  static const glass = Color(0x12FFFFFF);
  static const glassStrong = Color(0x1AFFFFFF);
  static const edge = Color(0x24FFFFFF);
  static const track = Color(0x1AFFFFFF);

  static ThemeData theme(String lang) {
    final base = ThemeData(
      brightness: Brightness.dark,
      scaffoldBackgroundColor: bg,
      colorScheme: const ColorScheme.dark(primary: accent, secondary: blue, surface: Color(0xFF121821), error: bad),
      useMaterial3: true,
    );
    return base.copyWith(
      textTheme: base.textTheme.apply(bodyColor: text, displayColor: text),
      dialogTheme: const DialogThemeData(backgroundColor: Color(0xFF151B24)),
      inputDecorationTheme: InputDecorationTheme(
        filled: true,
        fillColor: glass,
        border: OutlineInputBorder(borderRadius: BorderRadius.circular(16), borderSide: const BorderSide(color: edge)),
        enabledBorder: OutlineInputBorder(borderRadius: BorderRadius.circular(16), borderSide: const BorderSide(color: edge)),
        focusedBorder: OutlineInputBorder(borderRadius: BorderRadius.circular(16), borderSide: const BorderSide(color: accent)),
        labelStyle: const TextStyle(color: muted),
        hintStyle: const TextStyle(color: muted),
      ),
      snackBarTheme: const SnackBarThemeData(behavior: SnackBarBehavior.floating, backgroundColor: Color(0xFF1C2430)),
    );
  }
}

/// The colored backdrop the glass sits on.
class Backdrop extends StatelessWidget {
  const Backdrop({super.key, required this.child});
  final Widget child;

  @override
  Widget build(BuildContext context) {
    return DecoratedBox(
      decoration: const BoxDecoration(color: X.bg),
      child: Stack(children: [
        const Positioned.fill(child: _Glow(alignment: Alignment(-1.1, -1.0), color: Color(0xFF1E3A5F), size: 1.3)),
        const Positioned.fill(child: _Glow(alignment: Alignment(1.2, -0.2), color: Color(0xFF0F4C3A), size: 1.0)),
        const Positioned.fill(child: _Glow(alignment: Alignment(-0.2, 1.2), color: Color(0xFF3B1F5C), size: 1.2)),
        Positioned.fill(child: Material(type: MaterialType.transparency, child: child)),
      ]),
    );
  }
}

class _Glow extends StatelessWidget {
  const _Glow({required this.alignment, required this.color, required this.size});
  final Alignment alignment;
  final Color color;
  final double size;

  @override
  Widget build(BuildContext context) {
    return DecoratedBox(
      decoration: BoxDecoration(
        gradient: RadialGradient(center: alignment, radius: size, colors: [color, color.withValues(alpha: 0)], stops: const [0, 0.62]),
      ),
    );
  }
}

/// A frosted panel.
class Glass extends StatelessWidget {
  const Glass({super.key, required this.child, this.padding = const EdgeInsets.all(16), this.radius = 22, this.color, this.border, this.onTap});
  final Widget child;
  final EdgeInsetsGeometry padding;
  final double radius;
  final Color? color;
  final Color? border;
  final VoidCallback? onTap;

  @override
  Widget build(BuildContext context) {
    final r = BorderRadius.circular(radius);
    Widget body = Container(
      padding: padding,
      decoration: BoxDecoration(
        color: color ?? X.glass,
        borderRadius: r,
        border: Border.all(color: border ?? X.edge),
        gradient: LinearGradient(
          begin: Alignment.topCenter,
          end: Alignment.bottomCenter,
          colors: [Colors.white.withValues(alpha: 0.05), Colors.white.withValues(alpha: 0.0)],
          stops: const [0, 0.35],
        ),
      ),
      // list tiles and ink inside paint on this, above the panel's color
      child: Material(type: MaterialType.transparency, child: child),
    );
    if (onTap != null) {
      body = Material(type: MaterialType.transparency, child: InkWell(borderRadius: r, onTap: onTap, child: body));
    }
    return ClipRRect(
      borderRadius: r,
      child: BackdropFilter(filter: ImageFilter.blur(sigmaX: 24, sigmaY: 24), child: body),
    );
  }
}

class TabItem {
  const TabItem(this.icon, this.label);
  final IconData icon;
  final String label;
}

/// The floating pill tab bar.
class FloatingTabBar extends StatelessWidget {
  const FloatingTabBar({super.key, required this.items, required this.index, required this.onTap});
  final List<TabItem> items;
  final int index;
  final ValueChanged<int> onTap;

  @override
  Widget build(BuildContext context) {
    return Padding(
      padding: const EdgeInsets.fromLTRB(16, 0, 16, 16),
      child: DecoratedBox(
        decoration: BoxDecoration(
          borderRadius: BorderRadius.circular(32),
          boxShadow: const [BoxShadow(color: Color(0x73000000), blurRadius: 32, offset: Offset(0, 12))],
        ),
        child: Glass(
          radius: 32,
          padding: const EdgeInsets.all(6),
          color: X.glassStrong,
          child: Row(children: [
            for (var i = 0; i < items.length; i++)
              Expanded(
                child: Semantics(
                  selected: i == index,
                  button: true,
                  label: items[i].label,
                  child: InkWell(
                    borderRadius: BorderRadius.circular(26),
                    onTap: () => onTap(i),
                    child: AnimatedContainer(
                      duration: const Duration(milliseconds: 200),
                      padding: const EdgeInsets.symmetric(vertical: 7),
                      decoration: BoxDecoration(
                        color: i == index ? Colors.white.withValues(alpha: 0.14) : Colors.transparent,
                        borderRadius: BorderRadius.circular(26),
                      ),
                      child: Column(mainAxisSize: MainAxisSize.min, children: [
                        Icon(items[i].icon, size: 22, color: i == index ? X.accent : X.muted),
                        const SizedBox(height: 3),
                        Text(items[i].label,
                            maxLines: 1,
                            overflow: TextOverflow.ellipsis,
                            style: TextStyle(
                                fontSize: 11,
                                fontWeight: i == index ? FontWeight.w600 : FontWeight.w400,
                                color: i == index ? X.accent : X.muted)),
                      ]),
                    ),
                  ),
                ),
              ),
          ]),
        ),
      ),
    );
  }
}

/// A row of filter pills.
class Pills extends StatelessWidget {
  const Pills({super.key, required this.labels, required this.selected, required this.onSelect});
  final List<String> labels;
  final int selected;
  final ValueChanged<int> onSelect;

  @override
  Widget build(BuildContext context) {
    return SingleChildScrollView(
      scrollDirection: Axis.horizontal,
      child: Row(children: [
        for (var i = 0; i < labels.length; i++)
          Padding(
            padding: const EdgeInsetsDirectional.only(end: 8),
            child: i == selected
                ? FilledButton(
                    style: FilledButton.styleFrom(
                        backgroundColor: X.text, foregroundColor: X.bg, minimumSize: const Size(0, 36), shape: const StadiumBorder()),
                    onPressed: () => onSelect(i),
                    child: Text(labels[i]))
                : OutlinedButton(
                    style: OutlinedButton.styleFrom(
                        foregroundColor: X.text,
                        backgroundColor: X.glass,
                        side: const BorderSide(color: X.edge),
                        minimumSize: const Size(0, 36),
                        shape: const StadiumBorder()),
                    onPressed: () => onSelect(i),
                    child: Text(labels[i])),
          ),
      ]),
    );
  }
}

/// A two-way segmented control.
class Segmented extends StatelessWidget {
  const Segmented({super.key, required this.labels, required this.selected, required this.onSelect});
  final List<String> labels;
  final int selected;
  final ValueChanged<int> onSelect;

  @override
  Widget build(BuildContext context) {
    return Glass(
      radius: 18,
      padding: const EdgeInsets.all(4),
      child: Row(children: [
        for (var i = 0; i < labels.length; i++)
          Expanded(
            child: Semantics(
              selected: i == selected,
              button: true,
              child: InkWell(
                borderRadius: BorderRadius.circular(14),
                onTap: () => onSelect(i),
                child: AnimatedContainer(
                  duration: const Duration(milliseconds: 180),
                  height: 40,
                  alignment: Alignment.center,
                  decoration: BoxDecoration(
                    color: i == selected ? X.track : Colors.transparent,
                    borderRadius: BorderRadius.circular(14),
                  ),
                  child: Text(labels[i],
                      style: TextStyle(
                          fontSize: 14, fontWeight: i == selected ? FontWeight.w600 : FontWeight.w500, color: i == selected ? X.text : X.muted)),
                ),
              ),
            ),
          ),
      ]),
    );
  }
}

class Bar extends StatelessWidget {
  const Bar({super.key, required this.fraction, this.color = X.accent, this.height = 8});
  final double fraction;
  final Color color;
  final double height;

  @override
  Widget build(BuildContext context) {
    return ClipRRect(
      borderRadius: BorderRadius.circular(height / 2),
      child: SizedBox(
        height: height,
        child: Stack(children: [
          const Positioned.fill(child: ColoredBox(color: X.track)),
          Positioned.fill(
            child: Align(
              alignment: AlignmentDirectional.centerStart,
              child: FractionallySizedBox(widthFactor: fraction.clamp(0, 1), heightFactor: 1, child: ColoredBox(color: color)),
            ),
          ),
        ]),
      ),
    );
  }
}

class Badge2 extends StatelessWidget {
  const Badge2(this.text, this.color, {super.key});
  final String text;
  final Color color;

  @override
  Widget build(BuildContext context) {
    return Container(
      constraints: const BoxConstraints(minWidth: 62),
      padding: const EdgeInsets.symmetric(horizontal: 7, vertical: 3),
      decoration: BoxDecoration(color: color.withValues(alpha: 0.16), borderRadius: BorderRadius.circular(6)),
      child: Text(text, textAlign: TextAlign.center, style: TextStyle(fontSize: 10, fontWeight: FontWeight.w700, letterSpacing: 0.4, color: color)),
    );
  }
}

class SectionLabel extends StatelessWidget {
  const SectionLabel(this.text, {super.key});
  final String text;

  @override
  Widget build(BuildContext context) => Padding(
        padding: const EdgeInsetsDirectional.fromSTEB(4, 16, 4, 8),
        child: Text(text.toUpperCase(), style: const TextStyle(fontSize: 12, color: X.muted, letterSpacing: 0.6)),
      );
}

class ScreenTitle extends StatelessWidget {
  const ScreenTitle(this.text, {super.key, this.actions = const [], this.subtitle});
  final String text;
  final String? subtitle;
  final List<Widget> actions;

  @override
  Widget build(BuildContext context) {
    return Padding(
      padding: const EdgeInsetsDirectional.fromSTEB(20, 16, 16, 8),
      child: Row(children: [
        Expanded(
          child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
            Text(text, style: const TextStyle(fontSize: 26, fontWeight: FontWeight.w600, letterSpacing: -0.3)),
            if (subtitle != null) ...[
              const SizedBox(height: 2),
              Text(subtitle!, style: const TextStyle(fontSize: 13, color: X.muted)),
            ],
          ]),
        ),
        ...actions,
      ]),
    );
  }
}

class RoundIconButton extends StatelessWidget {
  const RoundIconButton({super.key, required this.icon, required this.tooltip, required this.onPressed, this.filled = false});
  final IconData icon;
  final String tooltip;
  final VoidCallback? onPressed;
  final bool filled;

  @override
  Widget build(BuildContext context) {
    return Padding(
      padding: const EdgeInsetsDirectional.only(start: 8),
      child: IconButton(
        tooltip: tooltip,
        onPressed: onPressed,
        icon: Icon(icon, size: 20),
        style: IconButton.styleFrom(
          minimumSize: const Size(44, 44),
          backgroundColor: filled ? X.accent : X.glass,
          foregroundColor: filled ? X.bg : X.text,
          side: filled ? null : const BorderSide(color: X.edge),
        ),
      ),
    );
  }
}
