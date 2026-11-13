#!/bin/sh
# Installs xtune for this user: ~/.local/opt/xtune, a launcher entry, and the xtune:// link handler.
set -eu
src=$(cd "$(dirname "$0")" && pwd)
dst="$HOME/.local/opt/xtune"
mkdir -p "$dst" "$HOME/.local/bin" "$HOME/.local/share/applications"
cp -r "$src"/. "$dst"/
ln -sf "$dst/xtune" "$HOME/.local/bin/xtune"
sed "s|^Exec=xtune|Exec=$dst/xtune|" "$src/xtune.desktop" > "$HOME/.local/share/applications/xtune.desktop"
command -v update-desktop-database >/dev/null && update-desktop-database "$HOME/.local/share/applications" || true
command -v xdg-mime >/dev/null && xdg-mime default xtune.desktop x-scheme-handler/xtune || true
echo "Installed to $dst. Start it from your applications menu, or run: xtune"
echo "Whole-device mode asks for your password (pkexec) to set up the network."
