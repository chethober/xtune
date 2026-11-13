import 'dart:async';
import 'dart:convert';
import 'dart:io';
import 'dart:math';

import 'package:flutter/services.dart';
import 'package:path_provider/path_provider.dart';

/// An error the core reported.
class CoreError implements Exception {
  CoreError(this.message);
  final String message;
  @override
  String toString() => message;
}

/// The Go core's API (core.Call): the same methods on every platform.
abstract class CoreClient {
  Future<dynamic> call(String method, [Map<String, dynamic> args = const {}]);

  /// Whether the whole-device mode needs the core restarted with administrator rights.
  bool get elevationForTun => false;
  bool get elevated => false;
  Future<void> ensureElevated() async {}

  static CoreClient? _instance;
  static CoreClient get instance => _instance ??= Platform.isAndroid ? AndroidCore() : DesktopCore();

  static dynamic unwrap(String reply) {
    final m = jsonDecode(reply) as Map<String, dynamic>;
    if (m['ok'] == true) return m['result'];
    throw CoreError((m['error'] ?? 'unknown error').toString());
  }
}

/// Android: the core is a gomobile library in this process; the VPN service lives in Kotlin.
class AndroidCore extends CoreClient {
  static const channel = MethodChannel('xtune/core');

  @override
  Future<dynamic> call(String method, [Map<String, dynamic> args = const {}]) async {
    final reply = await channel.invokeMethod<String>('call', {'method': method, 'args': jsonEncode(args)});
    return CoreClient.unwrap(reply ?? '{"ok":false,"error":"no reply"}');
  }

  /// Asks for the VPN permission if needed, then starts the VPN service with this connection.
  /// perAppMode: off, bypass (apps skip the VPN) or only (only apps use it).
  Future<dynamic> startVpn(Map<String, dynamic> profile, Map<String, dynamic> options, String perAppMode, List<String> apps) async {
    final reply = await channel.invokeMethod<String>('startVpn', {
      'args': jsonEncode({'profile': profile, 'options': options}),
      'perAppMode': perAppMode,
      'apps': apps,
    });
    return CoreClient.unwrap(reply ?? '{"ok":false,"error":"no reply"}');
  }

  Future<void> stopVpn() => channel.invokeMethod('stopVpn');

  /// Android-only actions: setAuto (boot and Wi-Fi auto-connect), askLocation, vpnSettings.
  Future<dynamic> native(String method, [Map<String, dynamic> args = const {}]) => channel.invokeMethod(method, args);

  Future<List<Map<String, String>>> installedApps() async {
    final l = await channel.invokeMethod<List<dynamic>>('apps') ?? [];
    return l.map((e) => Map<String, String>.from(e as Map)).toList();
  }
}

/// Desktops: the core is the xtune-core program next to the app, reached over a local port.
class DesktopCore extends CoreClient {
  Process? _proc;
  int? _port;
  String? _token;
  bool _elevated = false;
  Future<void>? _starting;
  final _http = HttpClient()..idleTimeout = const Duration(seconds: 30);

  @override
  bool get elevationForTun => true;
  @override
  bool get elevated => _elevated;

  /// The core program next to the app (the updater runs a copy of it).
  static String get coreExe => _exe;

  static String get _exe {
    final override = Platform.environment['XTUNE_CORE'];
    if (override != null && override.isNotEmpty) return override;
    final dir = File(Platform.resolvedExecutable).parent.path;
    final name = Platform.isWindows ? 'xtune-core.exe' : 'xtune-core';
    for (final p in ['$dir/$name', '$dir/lib/$name']) {
      if (File(p).existsSync()) return p;
    }
    return '$dir/$name';
  }

  static String _newToken() {
    final r = Random.secure();
    return List.generate(32, (_) => r.nextInt(16).toRadixString(16)).join();
  }

  Future<void> _start({required bool elevate}) async {
    await _stop();
    final tmp = await getTemporaryDirectory();
    final token = _newToken();
    final tokenFile = File('${tmp.path}/xtune-core-${DateTime.now().microsecondsSinceEpoch}.token');
    await tokenFile.writeAsString(token, flush: true);
    if (!Platform.isWindows) await Process.run('chmod', ['600', tokenFile.path]);
    final portFile = File('${tokenFile.path}.port');
    final args = ['--parent', '$pid', '--token-file', tokenFile.path, '--port-file', portFile.path];
    if (!elevate) {
      final p = await Process.start(_exe, args);
      _proc = p;
      final port = Completer<int>();
      p.stdout.transform(utf8.decoder).transform(const LineSplitter()).listen((l) {
        if (l.startsWith('LISTEN ') && !port.isCompleted) port.complete(int.parse(l.substring(7).trim()));
      });
      final err = StringBuffer();
      p.stderr.transform(utf8.decoder).listen(err.write);
      p.exitCode.then((c) {
        if (!port.isCompleted) port.completeError(CoreError('the engine stopped (exit $c): $err'));
        if (identical(_proc, p)) {
          _proc = null;
          _port = null;
        }
      });
      _port = await port.future.timeout(const Duration(seconds: 20));
    } else {
      // administrator rights for the TUN interface and routes
      if (Platform.isWindows) {
        final quoted = args.map((a) => "'${a.replaceAll("'", "''")}'").join(',');
        final r = await Process.run('powershell', [
          '-NoProfile',
          '-Command',
          "Start-Process -FilePath '${_exe.replaceAll("'", "''")}' -ArgumentList $quoted -Verb RunAs -WindowStyle Hidden",
        ]);
        if (r.exitCode != 0) throw CoreError('administrator rights were not granted');
      } else {
        final p = await Process.start('pkexec', [_exe, ...args, '--assets', File(_exe).parent.path]);
        _proc = p;
        p.exitCode.then((c) {
          if (identical(_proc, p)) {
            _proc = null;
            _port = null;
            _elevated = false;
          }
        });
      }
      for (var i = 0; i < 600; i++) {
        if (await portFile.exists()) {
          final s = (await portFile.readAsString()).trim();
          if (s.isNotEmpty) {
            _port = int.parse(s);
            break;
          }
        }
        if (!Platform.isWindows && _proc == null) throw CoreError('administrator rights were not granted');
        await Future<void>.delayed(const Duration(milliseconds: 200));
      }
      if (_port == null) throw CoreError('the engine did not start with administrator rights');
      portFile.delete().ignore();
    }
    _token = token;
    _elevated = elevate;
  }

  Future<void> _stop() async {
    if (_port != null) {
      try {
        await _post('quit', {}).timeout(const Duration(seconds: 5));
      } catch (_) {}
    }
    _proc?.kill();
    _proc = null;
    _port = null;
    _elevated = false;
  }

  Future<void> _ensure() async {
    if (_port != null) return;
    _starting ??= _start(elevate: false).whenComplete(() => _starting = null);
    await _starting;
  }

  @override
  Future<void> ensureElevated() async {
    if (_elevated && _port != null) return;
    await _start(elevate: true);
  }

  /// Back to an engine without administrator rights.
  Future<void> dropElevation() async {
    if (_elevated) await _start(elevate: false);
  }

  Future<String> _post(String method, Map<String, dynamic> args) async {
    final req = await _http.post('127.0.0.1', _port!, '/call');
    req.headers.set('Authorization', 'Bearer $_token');
    req.headers.contentType = ContentType.json;
    req.write(jsonEncode({'method': method, 'args': args}));
    final resp = await req.close();
    final body = await resp.transform(utf8.decoder).join();
    if (resp.statusCode != 200) throw CoreError('engine: HTTP ${resp.statusCode}');
    return body;
  }

  @override
  Future<dynamic> call(String method, [Map<String, dynamic> args = const {}]) async {
    await _ensure();
    try {
      return CoreClient.unwrap(await _post(method, args));
    } on SocketException {
      _port = null; // the engine died: start a new one and try once more
      await _ensure();
      return CoreClient.unwrap(await _post(method, args));
    }
  }

  Future<void> shutdown() => _stop();
}
