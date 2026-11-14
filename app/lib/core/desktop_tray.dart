import 'dart:io';

import 'package:flutter/foundation.dart';
import 'package:tray_manager/tray_manager.dart';
import 'package:window_manager/window_manager.dart';

import '../state/app_state.dart';
import '../ui/i18n.dart';
import 'core_client.dart';

/// Linux and Windows: an icon in the tray, and (with the "close to tray" setting) closing the
/// window hides it instead of quitting, so the connection is not dropped by a stray click. Quit
/// from the tray's menu. Launching xtune again brings the window back (the runners keep one
/// instance).
class DesktopTray {
  DesktopTray._();
  static Window? _window;
  static TrayIcon? _icon;
  static MenuItem? _toggle;
  static AppState? _state;
  static bool _quitting = false;
  static bool? _lastConnected;
  static String? _lastLang;

  static bool get supported => !kIsWeb && (Platform.isLinux || Platform.isWindows);

  /// Whether closing the window would hide it now: the setting is on and there is a tray icon to
  /// come back from.
  static bool get hidesOnClose => _icon != null && (_state?.settings.closeToTray ?? false) && !_quitting;

  static void init(AppState state) {
    if (!supported || _state != null) return;
    _state = state;
    try {
      _window = WindowManager.instance.getCurrent();
      _window?.addListener((e) {
        if (e is WindowCloseRequestedEvent && hidesOnClose) {
          e.request.cancel();
          _window?.hide();
        }
      });
    } catch (e) {
      debugPrint('tray: no window control: $e');
    }
    try {
      final icon = TrayIcon.create();
      if (icon == null) return;
      icon.icon = ImageAsset.fromAsset('assets/icon/tray.png');
      icon.addListener((e) {
        if (e is TrayIconClickedEvent || e is TrayIconDoubleClickedEvent) show();
      });
      _icon = icon;
      _buildMenu();
      icon.setContextMenuTrigger(ContextMenuTrigger.rightClicked);
      icon.setVisible(true);
      state.addListener(_refresh);
    } catch (e) {
      debugPrint('tray: no tray icon: $e');
      _icon = null;
    }
  }

  static void _buildMenu() {
    final icon = _icon, state = _state;
    if (icon == null || state == null) return;
    final menu = Menu.create();
    if (menu == null) return;
    MenuItem? item(String label, void Function() onClick) {
      final it = MenuItem.createWithLabelAndType(label, MenuItemType.normal);
      it?.addListener((e) {
        if (e is MenuItemClickedEvent) onClick();
      });
      return it;
    }

    menu.addItem(item(T.s('trayOpen'), show));
    _toggle = item(state.connected ? T.s('trayDisconnect') : T.s('trayConnect'), () {
      if (!state.busy && state.sub != null) state.toggle();
    });
    menu.addItem(_toggle);
    menu.addSeparator();
    menu.addItem(item(T.s('trayQuit'), quit));
    icon.setContextMenu(menu);
    _lastConnected = state.connected;
    _lastLang = T.lang;
    _tooltip();
  }

  static void _tooltip() {
    final state = _state;
    if (state == null) return;
    _icon?.setTooltip(state.connected ? 'xtune · ${T.s('connected')}' : 'xtune · ${T.s('notConnected')}');
  }

  static void _refresh() {
    final state = _state;
    if (state == null || _icon == null) return;
    if (_lastLang != T.lang) {
      _buildMenu();
      return;
    }
    if (_lastConnected != state.connected) {
      _lastConnected = state.connected;
      _toggle?.label = state.connected ? T.s('trayDisconnect') : T.s('trayConnect');
      _tooltip();
    }
  }

  static void show() {
    final w = _window;
    if (w == null) return;
    if (w.isMinimized) w.restore();
    w.show();
    w.focus();
  }

  /// Quits for real: stops the core (and with it the connection), then the app.
  static Future<void> quit() async {
    _quitting = true;
    try {
      final core = CoreClient.instance;
      if (core is DesktopCore) await core.shutdown().timeout(const Duration(seconds: 5));
    } catch (_) {}
    try {
      _icon?.setVisible(false);
      _icon?.dispose();
    } catch (_) {}
    exit(0);
  }
}
