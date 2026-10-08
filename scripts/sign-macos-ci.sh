#!/bin/bash
# Credentials come from CI secrets and remain in a temporary Keychain.
set -euo pipefail
app=${1:?Expected .app path}
: "${MACOS_CERTIFICATE_P12_BASE64:?}" "${MACOS_CERTIFICATE_PASSWORD:?}" "${MACOS_SIGNING_IDENTITY:?}"
[[ "$MACOS_SIGNING_IDENTITY" == 'Developer ID Application:'* ]] || { echo 'Public releases require a Developer ID Application identity.' >&2; exit 1; }
stage=$(mktemp -d)
keychain="$stage/signing.keychain-db"
password=$(openssl rand -hex 32)
# Existing login identities stay available; no key is copied into source/artifacts.
old_keychains=()
while IFS= read -r line; do line=${line//\"/}; line="$(printf '%s' "$line" | sed 's/^[[:space:]]*//;s/[[:space:]]*$//')"; [ -z "$line" ] || old_keychains+=("$line"); done < <(security list-keychains -d user)
cleanup() { security list-keychains -d user -s "${old_keychains[@]}" >/dev/null 2>&1 || true; security delete-keychain "$keychain" >/dev/null 2>&1 || true; rm -rf "$stage"; }
trap cleanup EXIT
printf '%s' "$MACOS_CERTIFICATE_P12_BASE64" | base64 -D > "$stage/certificate.p12"
chmod 600 "$stage/certificate.p12"
security create-keychain -p "$password" "$keychain"
security set-keychain-settings -lut 21600 "$keychain"
security unlock-keychain -p "$password" "$keychain"
security import "$stage/certificate.p12" -k "$keychain" -P "$MACOS_CERTIFICATE_PASSWORD" -T /usr/bin/codesign
security set-key-partition-list -S apple-tool:,apple:,codesign: -s -k "$password" "$keychain" >/dev/null
# Quote removal is only for Keychain paths returned by security, never credentials.
security list-keychains -d user -s "$keychain" "${old_keychains[@]}"
sh scripts/sign-macos.sh "$app"
if [ -z "${APPLE_API_KEY_BASE64:-}${APPLE_API_KEY_ID:-}${APPLE_API_ISSUER_ID:-}" ]; then
  echo 'Developer ID signature verified. Notarization credentials absent: this artifact is NOT notarized.'
  exit 0
fi
: "${APPLE_API_KEY_BASE64:?Notarization key required}" "${APPLE_API_KEY_ID:?}" "${APPLE_API_ISSUER_ID:?}"
printf '%s' "$APPLE_API_KEY_BASE64" | base64 -D > "$stage/notary.p8"
chmod 600 "$stage/notary.p8"
ditto -c -k --keepParent "$app" "$stage/notarize.zip"
xcrun notarytool submit "$stage/notarize.zip" --key "$stage/notary.p8" --key-id "$APPLE_API_KEY_ID" --issuer "$APPLE_API_ISSUER_ID" --wait --timeout 30m
xcrun stapler staple "$app"
xcrun stapler validate "$app"
spctl --assess --type execute --verbose=2 "$app"
