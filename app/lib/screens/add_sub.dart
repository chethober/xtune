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
  await showXSheet<void>(
    context,
    title: T.s('addSub'),
    builder: (ctx, setState) => Column(mainAxisSize: MainAxisSize.min, crossAxisAlignment: CrossAxisAlignment.stretch, children: [
      Text(T.s('noSubHint'), style: Type.secondary),
      const SizedBox(height: Space.l),
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
      const SizedBox(height: Space.l),
      Row(children: [
        if (Platform.isAndroid)
          OutlinedButton.icon(
            onPressed: () async {
              final code = await Navigator.of(ctx).push<String>(MaterialPageRoute(builder: (_) => const ScanPage()));
              if (code != null) setState(() => ctl.text = code);
            },
            icon: const Icon(Icons.qr_code_scanner_rounded),
            label: Text(T.s('scanQr')),
          ),
        const Spacer(),
        TextButton(onPressed: () => Navigator.pop(ctx), child: Text(T.s('cancel'))),
        const SizedBox(width: Space.s),
        FilledButton(
          style: FilledButton.styleFrom(minimumSize: const Size(96, 48)),
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
