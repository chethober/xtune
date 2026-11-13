import 'i18n.dart';

/// Numbers and dates in the app's language: Persian digits and the Persian (Jalali) calendar in
/// Persian, Latin digits and the Gregorian calendar otherwise.

const _fa = '۰۱۲۳۴۵۶۷۸۹';

/// [s] with its digits in the app's script.
String nums(String s) {
  if (T.lang != 'fa') return s;
  final b = StringBuffer();
  for (final r in s.runes) {
    b.write(r >= 0x30 && r <= 0x39 ? _fa[r - 0x30] : String.fromCharCode(r));
  }
  return b.toString();
}

/// The Jalali date of a Gregorian one: (year, month 1-12, day).
(int, int, int) toJalali(DateTime g) {
  const gdm = [0, 31, 59, 90, 120, 151, 181, 212, 243, 273, 304, 334];
  final gy = g.year, gm = g.month, gd = g.day;
  final gy2 = gm > 2 ? gy + 1 : gy;
  var days = 355666 + 365 * gy + (gy2 + 3) ~/ 4 - (gy2 + 99) ~/ 100 + (gy2 + 399) ~/ 400 + gd + gdm[gm - 1];
  var jy = -1595 + 33 * (days ~/ 12053);
  days %= 12053;
  jy += 4 * (days ~/ 1461);
  days %= 1461;
  if (days > 365) {
    jy += (days - 1) ~/ 365;
    days = (days - 1) % 365;
  }
  final int jm, jd;
  if (days < 186) {
    jm = 1 + days ~/ 31;
    jd = 1 + days % 31;
  } else {
    jm = 7 + (days - 186) ~/ 30;
    jd = 1 + (days - 186) % 30;
  }
  return (jy, jm, jd);
}

const _faMonths = ['فروردین', 'اردیبهشت', 'خرداد', 'تیر', 'مرداد', 'شهریور', 'مهر', 'آبان', 'آذر', 'دی', 'بهمن', 'اسفند'];
const _enMonths = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];

String _two(int n) => n.toString().padLeft(2, '0');

/// A full date: 2026-11-13, or ۱۴۰۵/۰۸/۲۲.
String fmtDate(DateTime d) {
  if (T.lang == 'fa') {
    final (y, m, day) = toJalali(d);
    return nums('$y/${_two(m)}/${_two(day)}');
  }
  return '${d.year}-${_two(d.month)}-${_two(d.day)}';
}

/// A short date for charts and lists: Nov 13, or ۲۲ آبان.
String fmtDay(DateTime d) {
  if (T.lang == 'fa') {
    final (_, m, day) = toJalali(d);
    return '${nums('$day')} ${_faMonths[m - 1]}';
  }
  return '${_enMonths[d.month - 1]} ${d.day}';
}

/// A time of day, 24-hour: 14:05.
String fmtTime(DateTime d) => nums('${_two(d.hour)}:${_two(d.minute)}');

/// A date and time for logs and the timeline.
String fmtDateTime(DateTime d) => '${fmtDay(d)} ${fmtTime(d)}';
