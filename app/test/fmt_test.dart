import 'package:flutter_test/flutter_test.dart';
import 'package:xtune_client/state/models.dart';
import 'package:xtune_client/ui/fmt.dart';
import 'package:xtune_client/ui/i18n.dart';

void main() {
  tearDown(() => T.lang = 'en');

  test('Jalali dates', () {
    expect(toJalali(DateTime(2026, 3, 21)), (1405, 1, 1)); // Nowruz
    expect(toJalali(DateTime(2026, 11, 13)), (1405, 8, 22));
    expect(toJalali(DateTime(2025, 3, 20)), (1403, 12, 30)); // 1403 is a leap year
    expect(toJalali(DateTime(2024, 3, 20)), (1403, 1, 1));
    expect(toJalali(DateTime(2027, 1, 1)), (1405, 10, 11));
  });

  test('numbers and dates follow the language', () {
    expect(fmtDate(DateTime(2026, 11, 13)), '2026-11-13');
    expect(fmtBytes(1536), '1.5 KB');
    T.lang = 'fa';
    expect(fmtDate(DateTime(2026, 11, 13)), '۱۴۰۵/۰۸/۲۲');
    expect(fmtDay(DateTime(2026, 11, 13)), '۲۲ آبان');
    expect(fmtBytes(1536), '۱.۵ KB');
    expect(fmtDuration(const Duration(minutes: 75, seconds: 3)), '۰۱:۱۵:۰۳');
    expect(T.s('tuneResults', {'n': 2, 'total': 12}), contains('۱۲'));
  });
}
