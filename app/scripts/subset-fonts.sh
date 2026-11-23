#!/bin/sh
# Shrinks the bundled Latin fonts in place (about 35% smaller): drops their hinting, which Flutter
# does not use, and the scripts the app never shows in them. Kept: Latin (with Vietnamese),
# Greek, Cyrillic, punctuation, arrows, math and the symbols the UI uses; other text falls back to
# Vazirmatn or the system's fonts, as before. Every OpenType feature stays (tnum for the figures).
# Run it again after replacing a font with a new release:  pip install fonttools && scripts/subset-fonts.sh
set -eu
fonts=$(cd "$(dirname "$0")/../app/assets/fonts" && pwd)
latin="U+0000-024F,U+0259,U+02B0-02FF,U+0300-036F,U+0370-03FF,U+0400-04FF,U+1E00-1EFF,U+2000-206F,U+2070-209F,U+20A0-20CF,U+2100-214F,U+2150-218F,U+2190-21FF,U+2200-22FF,U+2300-23FF,U+25A0-25FF,U+2600-26FF,U+2713-2717,U+FEFF,U+FFFD"
for f in "$fonts"/Inter-*.ttf "$fonts"/JetBrainsMono-*.ttf; do
  pyftsubset "$f" --unicodes="$latin" --layout-features='*' --no-hinting --glyph-names=false --name-IDs='*' \
    --output-file="$f.tmp"
  mv "$f.tmp" "$f"
done
