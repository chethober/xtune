import 'dart:ui';

import 'package:flutter/services.dart';
import 'package:material_ui/material_ui.dart';

import 'tokens.dart';

export 'tokens.dart';

/// The colored backdrop the glass sits on.
class Backdrop extends StatelessWidget {
  const Backdrop({super.key, required this.child});
  final Widget child;

  @override
  Widget build(BuildContext context) {
    return DecoratedBox(
      decoration: const BoxDecoration(color: X.bg),
      child: Stack(children: [
        const Positioned.fill(child: _Glow(alignment: Alignment(-1.1, -1.0), color: X.glowBlue, size: 1.3)),
        const Positioned.fill(child: _Glow(alignment: Alignment(1.2, -0.2), color: X.glowGreen, size: 1.0)),
        const Positioned.fill(child: _Glow(alignment: Alignment(-0.2, 1.2), color: X.glowPurple, size: 1.2)),
        // ink and list tiles paint on this, above the backdrop's color
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
  Widget build(BuildContext context) => DecoratedBox(
        decoration: BoxDecoration(
          gradient: RadialGradient(center: alignment, radius: size, colors: [color, color.withValues(alpha: 0)], stops: const [0, 0.62]),
        ),
      );
}

/// A frosted panel.
class Glass extends StatelessWidget {
  const Glass({super.key, required this.child, this.padding = const EdgeInsets.all(Space.l), this.radius = Radii.card, this.color, this.border, this.onTap});
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
      child: Material(type: MaterialType.transparency, child: child),
    );
    if (onTap != null) {
      body = Material(type: MaterialType.transparency, child: InkWell(borderRadius: r, onTap: onTap, child: body));
    }
    return ClipRRect(borderRadius: r, child: BackdropFilter(filter: ImageFilter.blur(sigmaX: 24, sigmaY: 24), child: body));
  }
}

/// A tinted panel for errors and warnings.
class Notice extends StatelessWidget {
  const Notice(this.text, {super.key, this.color = X.bad, this.icon = Icons.error_outline_rounded});
  final String text;
  final Color color;
  final IconData icon;

  @override
  Widget build(BuildContext context) => Glass(
        color: color.withValues(alpha: 0.12),
        border: color.withValues(alpha: 0.4),
        child: Row(children: [
          Icon(icon, color: color),
          const SizedBox(width: Space.m),
          Expanded(child: Text(text, style: Type.secondary.copyWith(color: X.text))),
        ]),
      );
}

// ---------------------------------------------------------------- page layout

/// A page: its header (title, subtitle, actions, a back button when pushed) and its content, with
/// the same margins everywhere and room for the floating tab bar.
class XPage extends StatelessWidget {
  const XPage({
    super.key,
    required this.title,
    this.subtitle,
    this.actions = const [],
    this.children = const [],
    this.onRefresh,
    this.back = false,
    this.leading,
  }) : body = null;

  /// A page whose body fills the rest of the screen (a console, a log) instead of scrolling.
  const XPage.fill({super.key, required this.title, this.subtitle, this.actions = const [], required Widget this.body, this.back = true, this.leading})
      : children = const [],
        onRefresh = null;

  final String title;
  final String? subtitle;
  final List<Widget> actions;
  final List<Widget> children;
  final Future<void> Function()? onRefresh;
  final bool back;
  final Widget? leading; // in place of the title (the home page's wordmark)
  final Widget? body;

  Widget _header(BuildContext context) => Padding(
        padding: const EdgeInsets.only(top: Space.l, bottom: Space.m),
        child: Row(children: [
          if (back)
            Padding(
              padding: const EdgeInsetsDirectional.only(end: Space.s),
              child: RoundIconButton(
                icon: Icons.arrow_back_rounded,
                tooltip: MaterialLocalizations.of(context).backButtonTooltip,
                onPressed: () => Navigator.of(context).maybePop(),
                margin: false,
              ),
            ),
          Expanded(
            child: leading ??
                Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
                  Text(title, style: Type.display),
                  if (subtitle != null) ...[
                    const SizedBox(height: 2),
                    Text(subtitle!, style: Type.secondary, maxLines: 2, overflow: TextOverflow.ellipsis),
                  ],
                ]),
          ),
          ...actions,
        ]),
      );

  @override
  Widget build(BuildContext context) {
    if (body != null) {
      return Padding(
        padding: const EdgeInsets.fromLTRB(Space.page, 0, Space.page, Space.l),
        child: Column(crossAxisAlignment: CrossAxisAlignment.stretch, children: [_header(context), Expanded(child: body!)]),
      );
    }
    final list = ListView(
      physics: const AlwaysScrollableScrollPhysics(),
      padding: EdgeInsets.fromLTRB(Space.page, 0, Space.page, back ? Space.xxl : Space.tabBar),
      children: [_header(context), ...children],
    );
    return onRefresh == null ? list : RefreshIndicator(onRefresh: onRefresh!, child: list);
  }
}

/// A pushed page (users, console, logs): the backdrop, a scaffold, and the page.
Future<T?> pushPage<T>(BuildContext context, Widget page) => Navigator.of(context).push<T>(PageRouteBuilder(
      transitionDuration: Motion.normal,
      reverseTransitionDuration: Motion.normal,
      pageBuilder: (_, _, _) => Scaffold(backgroundColor: Colors.transparent, body: Backdrop(child: SafeArea(child: page))),
      transitionsBuilder: (_, a, _, child) => FadeTransition(
        opacity: CurvedAnimation(parent: a, curve: Motion.curve),
        child: SlideTransition(position: Tween(begin: const Offset(0.06, 0), end: Offset.zero).animate(CurvedAnimation(parent: a, curve: Motion.curve)), child: child),
      ),
    ));

/// The bottom sheet every screen uses: frosted, with a title.
Future<T?> showXSheet<T>(BuildContext context, {String? title, required Widget Function(BuildContext, StateSetter) builder, bool padded = true}) {
  return showModalBottomSheet<T>(
    context: context,
    isScrollControlled: true,
    backgroundColor: Colors.transparent,
    builder: (ctx) => StatefulBuilder(
      builder: (ctx, setState) => Padding(
        padding: EdgeInsets.only(bottom: MediaQuery.viewInsetsOf(ctx).bottom),
        child: SafeArea(
          child: Padding(
            padding: const EdgeInsets.all(Space.m),
            child: ConstrainedBox(
              constraints: BoxConstraints(maxHeight: MediaQuery.sizeOf(ctx).height * 0.85),
              child: Glass(
                color: X.sheet,
                padding: padded ? const EdgeInsets.all(Space.xl) : const EdgeInsets.symmetric(vertical: Space.m),
                child: SingleChildScrollView(
                  child: Column(mainAxisSize: MainAxisSize.min, crossAxisAlignment: CrossAxisAlignment.stretch, children: [
                    if (title != null)
                      Padding(
                        padding: EdgeInsets.fromLTRB(padded ? 0 : Space.l, 0, padded ? 0 : Space.l, Space.m),
                        child: Text(title, style: Type.title),
                      ),
                    builder(ctx, setState),
                  ]),
                ),
              ),
            ),
          ),
        ),
      ),
    ),
  );
}

// ---------------------------------------------------------------- tab bar

class TabItem {
  const TabItem(this.icon, this.label);
  final IconData icon;
  final String label;
}

/// The floating pill tab bar. Its highlight slides under a finger dragged across it (and selects
/// where the finger lifts), and follows the pointer on desktops.
class FloatingTabBar extends StatefulWidget {
  const FloatingTabBar({super.key, required this.items, required this.index, required this.onTap});
  final List<TabItem> items;
  final int index;
  final ValueChanged<int> onTap;

  @override
  State<FloatingTabBar> createState() => _FloatingTabBarState();
}

class _FloatingTabBarState extends State<FloatingTabBar> {
  double? _drag; // the finger's x while dragging
  double? _hover; // the pointer's x on desktops
  int? _lastSlot;

  int _slotAt(double x, double slot) => (x / slot).floor().clamp(0, widget.items.length - 1);

  /// Visual slot for logical item i (right to left in Persian).
  int _visual(int i, bool rtl) => rtl ? widget.items.length - 1 - i : i;

  void _dragTo(double x, double slot, bool rtl) {
    setState(() => _drag = x);
    final s = _slotAt(x, slot);
    if (s != _lastSlot) {
      _lastSlot = s;
      HapticFeedback.selectionClick();
    }
  }

  void _release(double slot, bool rtl) {
    if (_drag == null) return;
    final i = _visual(_slotAt(_drag!, slot), rtl);
    setState(() {
      _drag = null;
      _lastSlot = null;
    });
    widget.onTap(i);
  }

  @override
  Widget build(BuildContext context) {
    final rtl = Directionality.of(context) == TextDirection.rtl;
    final n = widget.items.length;
    return Padding(
      padding: const EdgeInsets.fromLTRB(Space.l, 0, Space.l, Space.l),
      child: DecoratedBox(
        decoration: BoxDecoration(
          borderRadius: BorderRadius.circular(Radii.bar),
          boxShadow: const [BoxShadow(color: Color(0x73000000), blurRadius: 32, offset: Offset(0, 12))],
        ),
        child: Glass(
          radius: Radii.bar,
          padding: const EdgeInsets.all(6),
          color: X.glassStrong,
          child: LayoutBuilder(builder: (context, c) {
            final slot = c.maxWidth / n;
            final dragging = _drag != null;
            final pillLeft = dragging ? (_drag! - slot / 2).clamp(0.0, c.maxWidth - slot) : _visual(widget.index, rtl) * slot;
            final litSlot = dragging ? _slotAt(_drag!, slot) : _visual(widget.index, rtl);
            return MouseRegion(
              onHover: (e) => setState(() => _hover = e.localPosition.dx),
              onExit: (_) => setState(() => _hover = null),
              child: GestureDetector(
                behavior: HitTestBehavior.opaque,
                onHorizontalDragStart: (d) => _dragTo(d.localPosition.dx, slot, rtl),
                onHorizontalDragUpdate: (d) => _dragTo(d.localPosition.dx, slot, rtl),
                onHorizontalDragEnd: (_) => _release(slot, rtl),
                onHorizontalDragCancel: () => setState(() => _drag = null),
                onTapUp: (d) => widget.onTap(_visual(_slotAt(d.localPosition.dx, slot), rtl)),
                child: SizedBox(
                  height: 54,
                  child: Stack(children: [
                    if (_hover != null && !dragging)
                      AnimatedPositioned(
                        duration: Motion.fast,
                        curve: Motion.curve,
                        left: _slotAt(_hover!, slot) * slot,
                        width: slot,
                        top: 0,
                        bottom: 0,
                        child: DecoratedBox(
                          decoration: BoxDecoration(color: Colors.white.withValues(alpha: 0.06), borderRadius: BorderRadius.circular(26)),
                        ),
                      ),
                    AnimatedPositioned(
                      duration: dragging ? Duration.zero : Motion.normal,
                      curve: Motion.curve,
                      left: pillLeft,
                      width: slot,
                      top: 0,
                      bottom: 0,
                      child: AnimatedScale(
                        duration: Motion.fast,
                        scale: dragging ? 1.06 : 1,
                        child: DecoratedBox(
                          decoration: BoxDecoration(
                            color: X.selected,
                            borderRadius: BorderRadius.circular(26),
                            border: Border.all(color: Colors.white.withValues(alpha: dragging ? 0.18 : 0.06)),
                          ),
                        ),
                      ),
                    ),
                    for (var v = 0; v < n; v++)
                      Positioned(
                        left: v * slot,
                        width: slot,
                        top: 0,
                        bottom: 0,
                        child: _TabLabel(item: widget.items[_visual(v, rtl)], lit: v == litSlot, selected: _visual(v, rtl) == widget.index),
                      ),
                  ]),
                ),
              ),
            );
          }),
        ),
      ),
    );
  }
}

class _TabLabel extends StatelessWidget {
  const _TabLabel({required this.item, required this.lit, required this.selected});
  final TabItem item;
  final bool lit, selected;

  @override
  Widget build(BuildContext context) {
    final color = lit ? X.accent : X.muted;
    return Semantics(
      button: true,
      selected: selected,
      label: item.label,
      excludeSemantics: true,
      child: Column(mainAxisAlignment: MainAxisAlignment.center, children: [
        AnimatedScale(duration: Motion.fast, scale: lit ? 1.08 : 1, child: Icon(item.icon, size: 22, color: color)),
        const SizedBox(height: 3),
        AnimatedDefaultTextStyle(
          duration: Motion.fast,
          style: Type.tab.copyWith(color: color, fontWeight: lit ? FontWeight.w600 : FontWeight.w400),
          child: Text(item.label, maxLines: 1, overflow: TextOverflow.ellipsis),
        ),
      ]),
    );
  }
}

// ---------------------------------------------------------------- controls

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
            padding: const EdgeInsetsDirectional.only(end: Space.s),
            child: Semantics(
              selected: i == selected,
              button: true,
              child: Material(
                color: Colors.transparent,
                child: InkWell(
                  borderRadius: BorderRadius.circular(18),
                  onTap: () => onSelect(i),
                  child: AnimatedContainer(
                    duration: Motion.fast,
                    height: 36,
                    padding: const EdgeInsets.symmetric(horizontal: 14),
                    alignment: Alignment.center,
                    decoration: BoxDecoration(
                      color: i == selected ? X.text : X.glass,
                      borderRadius: BorderRadius.circular(18),
                      border: Border.all(color: i == selected ? X.text : X.edge),
                    ),
                    child: Text(labels[i], style: Type.secondary.copyWith(color: i == selected ? X.bg : X.text, fontWeight: FontWeight.w500)),
                  ),
                ),
              ),
            ),
          ),
      ]),
    );
  }
}

/// A segmented control whose highlight slides between segments.
class Segmented extends StatelessWidget {
  const Segmented({super.key, required this.labels, required this.selected, required this.onSelect});
  final List<String> labels;
  final int selected;
  final ValueChanged<int> onSelect;

  @override
  Widget build(BuildContext context) {
    final rtl = Directionality.of(context) == TextDirection.rtl;
    final n = labels.length;
    return Glass(
      radius: Radii.l,
      padding: const EdgeInsets.all(Space.xs),
      child: LayoutBuilder(builder: (context, c) {
        final w = c.maxWidth / n;
        final v = rtl ? n - 1 - selected : selected;
        return SizedBox(
          height: 40,
          child: Stack(children: [
            AnimatedPositioned(
              duration: Motion.normal,
              curve: Motion.curve,
              left: v * w,
              width: w,
              top: 0,
              bottom: 0,
              child: DecoratedBox(decoration: BoxDecoration(color: X.selected, borderRadius: BorderRadius.circular(Radii.m))),
            ),
            Row(children: [
              for (var i = 0; i < n; i++)
                Expanded(
                  child: Semantics(
                    selected: i == selected,
                    button: true,
                    child: InkWell(
                      borderRadius: BorderRadius.circular(Radii.m),
                      onTap: () => onSelect(i),
                      child: Center(
                        child: AnimatedDefaultTextStyle(
                          duration: Motion.fast,
                          style: Type.body.copyWith(fontSize: 14, color: i == selected ? X.text : X.muted, fontWeight: i == selected ? FontWeight.w600 : FontWeight.w500),
                          child: Text(labels[i], maxLines: 1, overflow: TextOverflow.ellipsis),
                        ),
                      ),
                    ),
                  ),
                ),
            ]),
          ]),
        );
      }),
    );
  }
}

/// A progress or quota bar that animates to its value.
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
            child: TweenAnimationBuilder<double>(
              tween: Tween(end: fraction.clamp(0, 1).toDouble()),
              duration: Motion.slow,
              curve: Motion.curve,
              builder: (_, f, _) => Align(
                alignment: AlignmentDirectional.centerStart,
                child: FractionallySizedBox(widthFactor: f, heightFactor: 1, child: ColoredBox(color: color)),
              ),
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
  Widget build(BuildContext context) => Container(
        constraints: const BoxConstraints(minWidth: 62),
        padding: const EdgeInsets.symmetric(horizontal: 7, vertical: 3),
        decoration: BoxDecoration(color: color.withValues(alpha: 0.16), borderRadius: BorderRadius.circular(6)),
        child: Text(text, textAlign: TextAlign.center, style: Type.badge.copyWith(color: color)),
      );
}

class Dot extends StatelessWidget {
  const Dot(this.color, {super.key, this.size = 8});
  final Color color;
  final double size;
  @override
  Widget build(BuildContext context) => AnimatedContainer(
        duration: Motion.normal,
        width: size,
        height: size,
        decoration: BoxDecoration(color: color, shape: BoxShape.circle),
      );
}

class SectionLabel extends StatelessWidget {
  const SectionLabel(this.text, {super.key});
  final String text;

  @override
  Widget build(BuildContext context) => Padding(
        padding: const EdgeInsetsDirectional.fromSTEB(Space.xs, Space.l, Space.xs, Space.s),
        child: Text(text.toUpperCase(), style: Type.overline),
      );
}

class RoundIconButton extends StatelessWidget {
  const RoundIconButton({super.key, required this.icon, required this.tooltip, required this.onPressed, this.filled = false, this.margin = true});
  final IconData icon;
  final String tooltip;
  final VoidCallback? onPressed;
  final bool filled;
  final bool margin;

  @override
  Widget build(BuildContext context) {
    return Padding(
      padding: EdgeInsetsDirectional.only(start: margin ? Space.s : 0),
      child: IconButton(
        tooltip: tooltip,
        onPressed: onPressed,
        icon: Icon(icon, size: 20),
        style: IconButton.styleFrom(
          minimumSize: const Size(44, 44),
          backgroundColor: filled ? X.accent : X.glass,
          foregroundColor: filled ? X.onAccent : X.text,
          side: filled ? null : const BorderSide(color: X.edge),
        ),
      ),
    );
  }
}

/// A grouped list of rows on one panel, divided by hairlines.
class Group extends StatelessWidget {
  const Group({super.key, required this.children});
  final List<Widget> children;

  @override
  Widget build(BuildContext context) => Glass(
        padding: EdgeInsets.zero,
        child: Column(children: [
          for (var i = 0; i < children.length; i++) ...[
            if (i > 0) const Divider(),
            children[i],
          ],
        ]),
      );
}
