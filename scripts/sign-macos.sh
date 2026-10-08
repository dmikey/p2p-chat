#!/bin/sh
# Uses an identity already in the caller's Keychain. Never exports private keys.
set -eu
app=${1:?Usage: sign-macos.sh path/to/app.app}
identity=${MACOS_SIGNING_IDENTITY:?Set MACOS_SIGNING_IDENTITY to your Keychain identity}
case "$identity" in
 'Developer ID Application:'*) ;;
 'Apple Development:'*)
  [ "${RADCHAT_ALLOW_DEVELOPMENT_SIGNING:-0}" = 1 ] || { echo 'Development signing requires RADCHAT_ALLOW_DEVELOPMENT_SIGNING=1; this does not make a notarized public release.' >&2; exit 1; } ;;
 *) echo 'Use a Developer ID Application identity, or explicitly opt into local Apple Development signing.' >&2; exit 1 ;;
esac
[ -d "$app/Contents" ] || { echo 'Expected a packaged .app bundle.' >&2; exit 1; }
codesign --force --options runtime --timestamp --sign "$identity" "$app"
codesign --verify --deep --strict --verbose=2 "$app"
codesign --display --verbose=2 "$app" 2>&1
