#!/bin/sh
set -eu
# Install from an extracted release bundle. Downloads are handled separately.
bundle=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
install_dir=${RADCHAT_INSTALL_DIR:-"$HOME/.local/bin"}
mkdir -p "$install_dir"
install -m 755 "$bundle/radchat" "$install_dir/radchat"
printf 'Installed %s/radchat\n' "$install_dir"
printf 'Start your device: %s/radchat node\n' "$install_dir"
printf 'Self-hosted relay setup: see deploy/ and README.md in this bundle.\n'
printf 'Add %s to your PATH if needed.\n' "$install_dir"
