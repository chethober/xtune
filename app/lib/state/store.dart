import 'dart:convert';
import 'dart:io';

import 'package:flutter_secure_storage/flutter_secure_storage.dart';
import 'package:path_provider/path_provider.dart';
import 'package:shared_preferences/shared_preferences.dart';

/// Plain settings and subscriptions (no server secrets) in shared preferences.
class Prefs {
  Prefs._(this._p);
  final SharedPreferences _p;
  static Future<Prefs> open() async => Prefs._(await SharedPreferences.getInstance());

  dynamic getJson(String key) {
    final s = _p.getString(key);
    if (s == null) return null;
    try {
      return jsonDecode(s);
    } catch (_) {
      return null;
    }
  }

  Future<void> setJson(String key, Object? v) => _p.setString(key, jsonEncode(v));
}

/// Server passwords and keys: the system keystore, or, where there is none (a Linux desktop
/// without a keyring), a file only this user can read.
class Secrets {
  static const _secure = FlutterSecureStorage();
  static bool _fallback = false;

  static Future<File> _file(String key) async {
    final d = await getApplicationSupportDirectory();
    final dir = Directory('${d.path}/secrets');
    if (!await dir.exists()) {
      await dir.create(recursive: true);
      if (!Platform.isWindows) await Process.run('chmod', ['700', dir.path]);
    }
    return File('${dir.path}/${key.replaceAll(RegExp(r'[^A-Za-z0-9_-]'), '_')}');
  }

  static Future<String?> read(String key) async {
    if (!_fallback) {
      try {
        return await _secure.read(key: key);
      } catch (_) {
        _fallback = true;
      }
    }
    final f = await _file(key);
    return await f.exists() ? f.readAsString() : null;
  }

  static Future<void> write(String key, String value) async {
    if (!_fallback) {
      try {
        await _secure.write(key: key, value: value);
        return;
      } catch (_) {
        _fallback = true;
      }
    }
    final f = await _file(key);
    await f.writeAsString(value, flush: true);
    if (!Platform.isWindows) await Process.run('chmod', ['600', f.path]);
  }

  static Future<void> delete(String key) async {
    try {
      await _secure.delete(key: key);
    } catch (_) {}
    final f = await _file(key);
    if (await f.exists()) await f.delete();
  }
}
