import 'package:material_ui/material_ui.dart';

import '../state/models.dart';
import 'fmt.dart';
import 'glass.dart';
import 'i18n.dart';

/// Traffic per day as bars, with the day under the finger or pointer read out above them.
class DailyBars extends StatefulWidget {
  const DailyBars({super.key, required this.days, this.height = 110});
  final List<(DateTime, int)> days; // oldest first
  final double height;
  @override
  State<DailyBars> createState() => _DailyBarsState();
}

class _DailyBarsState extends State<DailyBars> {
  int? hover;

  @override
  Widget build(BuildContext context) {
    final d = widget.days;
    if (d.isEmpty) return const SizedBox.shrink();
    final top = d.fold<int>(1, (a, x) => x.$2 > a ? x.$2 : a);
    final shown = hover != null && hover! < d.length ? d[hover!] : null;
    final h = widget.height;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        SizedBox(
          height: 20,
          child: Text(
            shown == null ? T.s('perDay') : '${fmtDate(shown.$1)} · ${fmtBytes(shown.$2)}',
            style: shown == null ? Type.caption : Type.number,
          ),
        ),
        const SizedBox(height: Space.xs),
        SizedBox(
          height: h,
          child: LayoutBuilder(
            builder: (context, c) {
              final slot = c.maxWidth / d.length;
              int at(double x) => (x / slot).floor().clamp(0, d.length - 1);
              return GestureDetector(
                behavior: HitTestBehavior.opaque,
                onTapDown: (e) => setState(() => hover = at(e.localPosition.dx)),
                onHorizontalDragUpdate: (e) => setState(() => hover = at(e.localPosition.dx)),
                onHorizontalDragEnd: (_) => setState(() => hover = null),
                child: Row(
                  crossAxisAlignment: CrossAxisAlignment.end,
                  children: [
                    for (var i = 0; i < d.length; i++)
                      SizedBox(
                        width: slot,
                        child: Padding(
                          padding: const EdgeInsets.symmetric(horizontal: 1),
                          child: Align(
                            alignment: Alignment.bottomCenter,
                            child: Container(
                              height: d[i].$2 == 0 ? 2 : ((h - 4) * d[i].$2 / top).clamp(3, h - 4),
                              decoration: BoxDecoration(
                                color: d[i].$2 == 0 ? X.track : X.accent.withValues(alpha: hover == null || hover == i ? 1 : 0.45),
                                borderRadius: const BorderRadius.vertical(top: Radius.circular(3)),
                              ),
                            ),
                          ),
                        ),
                      ),
                  ],
                ),
              );
            },
          ),
        ),
        const SizedBox(height: Space.xs),
        Row(
          children: [
            Text(fmtDay(d.first.$1), style: Type.caption),
            const Spacer(),
            Text(T.s('today'), style: Type.caption),
          ],
        ),
      ],
    );
  }
}

/// The last [n] days of a {YYYY-MM-DD: bytes} map, oldest first, missing days as zero.
List<(DateTime, int)> lastDays(Map<String, dynamic>? daily, int n) {
  final now = DateTime.now();
  String key(DateTime d) => '${d.year}-${d.month.toString().padLeft(2, '0')}-${d.day.toString().padLeft(2, '0')}';
  return [
    for (var i = n - 1; i >= 0; i--)
      (DateTime(now.year, now.month, now.day - i), ((daily ?? const {})[key(DateTime(now.year, now.month, now.day - i))] as num?)?.toInt() ?? 0),
  ];
}
