#!/usr/bin/env python3
"""Fails when a relative link or #anchor in the Markdown docs points nowhere."""
import glob
import os
import re
import sys


def anchors(path):
    out = set()
    for line in open(path, encoding="utf-8"):
        if line.startswith("#"):
            t = line.lstrip("#").strip().lower()
            out.add(re.sub(r"[^\w\- ]", "", t).replace(" ", "-"))
    return out


bad = 0
for md in sorted(glob.glob("*.md") + glob.glob("docs/*.md")):
    for m in re.finditer(r"\]\(([^)\s]+)\)", open(md, encoding="utf-8").read()):
        url = m.group(1)
        if re.match(r"[a-z]+:", url):
            continue
        path, _, frag = url.partition("#")
        target = os.path.normpath(os.path.join(os.path.dirname(md), path)) if path else md
        if not os.path.exists(target):
            print(f"{md}: {url}: no such file")
            bad += 1
        elif frag and target.endswith(".md") and frag not in anchors(target):
            print(f"{md}: {url}: no such heading")
            bad += 1
sys.exit(1 if bad else 0)
