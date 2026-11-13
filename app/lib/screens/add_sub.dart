import 'dart:io';

import 'package:flutter/services.dart';
import 'package:material_ui/material_ui.dart';
import 'package:mobile_scanner/mobile_scanner.dart';

import '../main.dart';
import '../ui/glass.dart';
import '../ui/i18n.dart';

/// Asks for a subscription link (typed, pasted or scanned) and adds it.
Future<void> showAddSub(BuildContext context, {String? initial}) async {
  final state = AppScope.of(context);
  final ctl = TextEditingController(text: initial ?? '');
  if (initial == null) {
    final clip = await Clipboard.getData(Clipboard.kTextPlain);
    if (clip?.text?.trim().startsWith('http') == true || clip?.text?.trim().startsWith('xtune://') == true) {
      ctl.text = clip!.text!.trim();
    }
  }
  if (!context.mounted) return;
  String? err;
  var busy = false;
  await showModalBottomSheet<void>(
    context: context,
    isScrollControlled: true,
    backgroundColor: Colors.transparent,
    builder: (ctx) => StatefulBuilder(
      builder: (ctx, setState) => Padding(
        padding: EdgeInsets.only(bottom: MediaQuery.viewInsetsOf(ctx).bottom),
        child: Padding(
          padding: const EdgeInsets.all(12),
          child: Glass(
            color: const Color(0xE6151B24),
            padding: const EdgeInsets.all(20),
            child: Column(mainAxisSize: MainAxisSize.min, crossAxisAlignment: CrossAxisAlignment.stretch, children: [
              Text(T.s('addSub'), style: const TextStyle(fontSize: 20, fontWeight: FontWeight.w600)),
              const SizedBox(height: 6),
              Text(T.s('noSubHint'), style: const TextStyle(color: X.muted, fontSize: 13)),
              const SizedBox(height: 16),
              TextField(
                controller: ctl,
                autofocus: ctl.text.isEmpty,
                keyboardType: TextInputType.url,
                textDirection: TextDirection.ltr,
                decoration: InputDecoration(
                  labelText: T.s('subLink'),
                  hintText: 'https://…',
                  errorText: err,
                  errorMaxLines: 3,
                  suffixIcon: IconButton(
                    tooltip: T.s('paste'),
                    icon: const Icon(Icons.content_paste_rounded),
                    onPressed: () async {
                      final c = await Clipboard.getData(Clipboard.kTextPlain);
                      if (c?.text != null) ctl.text = c!.text!.trim();
                    },
                  ),
                ),
              ),
              const SizedBox(height: 16),
              Row(children: [
                if (Platform.isAndroid)
                  OutlinedButton.icon(
                    style: OutlinedButton.styleFrom(minimumSize: const Size(0, 48), foregroundColor: X.text, side: const BorderSide(color: X.edge)),
                    onPressed: () async {
                      final code = await Navigator.of(ctx).push<String>(MaterialPageRoute(builder: (_) => const ScanPage()));
                      if (code != null) setState(() => ctl.text = code);
                    },
                    icon: const Icon(Icons.qr_code_scanner_rounded),
                    label: Text(T.s('scanQr')),
                  ),
                const Spacer(),
                TextButton(onPressed: () => Navigator.pop(ctx), child: Text(T.s('cancel'))),
                const SizedBox(width: 8),
                FilledButton(
                  style: FilledButton.styleFrom(minimumSize: const Size(96, 48), backgroundColor: X.accent, foregroundColor: X.bg),
                  onPressed: busy
                      ? null
                      : () async {
                          setState(() {
                            busy = true;
                            err = null;
                          });
                          try {
                            await state.addSub(ctl.text);
                            if (ctx.mounted) Navigator.pop(ctx);
                          } catch (e) {
                            setState(() => err = e.toString());
                          } finally {
                            if (ctx.mounted) setState(() => busy = false);
                          }
                        },
                  child: busy ? const SizedBox(width: 18, height: 18, child: CircularProgressIndicator(strokeWidth: 2)) : Text(T.s('add')),
                ),
              ]),
            ]),
          ),
        ),
      ),
    ),
  );
}

class ScanPage extends StatefulWidget {
  const ScanPage({super.key});
  @override
  State<ScanPage> createState() => _ScanPageState();
}

class _ScanPageState extends State<ScanPage> {
  bool done = false;

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      appBar: AppBar(title: Text(T.s('scanQr')), backgroundColor: Colors.black),
      backgroundColor: Colors.black,
      body: MobileScanner(
        onDetect: (capture) {
          if (done) return;
          for (final b in capture.barcodes) {
            final v = b.rawValue;
            if (v != null && (v.startsWith('http') || v.startsWith('xtune://'))) {
              done = true;
              Navigator.pop(context, v);
              return;
            }
          }
        },
      ),
    );
  }
}
