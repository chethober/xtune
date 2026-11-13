import 'dart:math' as math;

import 'package:flutter/services.dart';
import 'package:material_ui/material_ui.dart';

import 'tokens.dart';

/// The connect toggle. It presses in under the finger and springs back, spins an arc while
/// connecting, and breathes a soft glow while connected.
class PowerButton extends StatefulWidget {
  const PowerButton({super.key, required this.on, required this.busy, required this.onTap, required this.label});
  final bool on, busy;
  final VoidCallback? onTap;
  final String label;

  @override
  State<PowerButton> createState() => _PowerButtonState();
}

class _PowerButtonState extends State<PowerButton> with TickerProviderStateMixin {
  late final _spin = AnimationController(vsync: this, duration: const Duration(milliseconds: 1100));
  late final _breath = AnimationController(vsync: this, duration: const Duration(milliseconds: 2600));
  late final _press = AnimationController(vsync: this, duration: Motion.fast, lowerBound: 0, upperBound: 1);
  late final _ripple = AnimationController(vsync: this, duration: const Duration(milliseconds: 700));

  @override
  void initState() {
    super.initState();
    _sync();
  }

  @override
  void didUpdateWidget(PowerButton old) {
    super.didUpdateWidget(old);
    if (old.on != widget.on && widget.on) _ripple.forward(from: 0); // a ring goes out on connect
    if (old.on != widget.on) HapticFeedback.mediumImpact();
    _sync();
  }

  void _sync() {
    widget.busy ? _spin.repeat() : _spin.stop();
    if (widget.on && !widget.busy) {
      if (!_breath.isAnimating) _breath.repeat(reverse: true);
    } else {
      _breath.stop();
      _breath.value = 0;
    }
  }

  @override
  void dispose() {
    _spin.dispose();
    _breath.dispose();
    _press.dispose();
    _ripple.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final on = widget.on;
    return Semantics(
      button: true,
      label: widget.label,
      child: GestureDetector(
        onTapDown: widget.onTap == null ? null : (_) => _press.forward(),
        onTapCancel: () => _press.reverse(),
        onTapUp: widget.onTap == null
            ? null
            : (_) {
                _press.animateBack(0, duration: Motion.slow, curve: Motion.spring);
                HapticFeedback.lightImpact();
                widget.onTap!();
              },
        child: AnimatedBuilder(
          animation: Listenable.merge([_spin, _breath, _press, _ripple]),
          builder: (context, _) {
            final breath = Curves.easeInOut.transform(_breath.value);
            final scale = 1 - 0.06 * _press.value;
            return SizedBox(
              width: 232,
              height: 232,
              child: Stack(
                alignment: Alignment.center,
                children: [
                  // the ring that goes out when the connection comes up
                  if (_ripple.isAnimating)
                    Opacity(
                      opacity: (1 - _ripple.value).clamp(0, 1),
                      child: Container(
                        width: 150 + 82 * Curves.easeOut.transform(_ripple.value),
                        height: 150 + 82 * Curves.easeOut.transform(_ripple.value),
                        decoration: BoxDecoration(
                          shape: BoxShape.circle,
                          border: Border.all(color: X.accent, width: 2),
                        ),
                      ),
                    ),
                  Transform.scale(
                    scale: scale,
                    child: AnimatedContainer(
                      duration: Motion.slow,
                      curve: Motion.curve,
                      width: 208,
                      height: 208,
                      decoration: BoxDecoration(
                        shape: BoxShape.circle,
                        gradient: on
                            ? RadialGradient(
                                colors: [
                                  X.accent.withValues(alpha: 0.18 + 0.12 * breath),
                                  X.accent.withValues(alpha: 0.04),
                                  Colors.transparent,
                                ],
                                stops: const [0, 0.62, 0.7],
                              )
                            : null,
                        color: on ? null : X.glass,
                        border: Border.all(color: on ? X.accent.withValues(alpha: 0.3 + 0.2 * breath) : X.edge),
                      ),
                      alignment: Alignment.center,
                      child: Stack(
                        alignment: Alignment.center,
                        children: [
                          if (widget.busy)
                            Transform.rotate(
                              angle: _spin.value * 2 * math.pi,
                              child: const SizedBox(width: 176, height: 176, child: CustomPaint(painter: _ArcPainter())),
                            ),
                          AnimatedContainer(
                            duration: Motion.slow,
                            curve: Motion.curve,
                            width: 150,
                            height: 150,
                            decoration: BoxDecoration(
                              shape: BoxShape.circle,
                              color: on ? X.accent : X.track,
                              boxShadow: on
                                  ? [
                                      BoxShadow(
                                        color: X.accent.withValues(alpha: 0.25 + 0.2 * breath),
                                        blurRadius: 30 + 20 * breath,
                                      ),
                                    ]
                                  : null,
                            ),
                            child: AnimatedSwitcher(
                              duration: Motion.normal,
                              transitionBuilder: (c, a) => ScaleTransition(
                                scale: a,
                                child: FadeTransition(opacity: a, child: c),
                              ),
                              child: Icon(
                                Icons.power_settings_new_rounded,
                                key: ValueKey(on),
                                size: 64,
                                color: on ? X.onAccent : (widget.busy ? X.accent : X.muted),
                              ),
                            ),
                          ),
                        ],
                      ),
                    ),
                  ),
                ],
              ),
            );
          },
        ),
      ),
    );
  }
}

/// A sweeping arc that fades out along its tail.
class _ArcPainter extends CustomPainter {
  const _ArcPainter();

  @override
  void paint(Canvas canvas, Size size) {
    final rect = Offset.zero & size;
    final paint = Paint()
      ..style = PaintingStyle.stroke
      ..strokeWidth = 3.5
      ..strokeCap = StrokeCap.round
      ..shader = SweepGradient(colors: [X.accent.withValues(alpha: 0), X.accent], stops: const [0.55, 1]).createShader(rect);
    canvas.drawArc(rect.deflate(2), 0, math.pi * 1.6, false, paint);
  }

  @override
  bool shouldRepaint(covariant CustomPainter oldDelegate) => false;
}
