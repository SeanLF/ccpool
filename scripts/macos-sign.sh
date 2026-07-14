#!/usr/bin/env bash
# Sign + notarize one macOS binary with Apple's own tooling (codesign + notarytool).
#
# WHY this exists: GoReleaser's built-in `notarize.macos` signs with quill so it can run on a Linux
# runner. quill's Developer ID signatures "do not satisfy their designated Requirement", and AMFI
# rejects them at exec on some macOS builds -- "Broken signature with Team ID fatal" -> SIGKILL
# before main() (v0.1.1 shipped exactly this; see docs/DECISIONS.md). Running on a macOS runner and
# signing with Apple's real `codesign` produces a signature AMFI accepts everywhere.
#
# Called once per built binary from .goreleaser.yaml as a post-build hook:
#   ./scripts/macos-sign.sh <binary-path> <goos> <goarch>
#
# Fails OPEN when unsigned (forks / local snapshots have no identity): leaves Go's default ad-hoc
# signature, which still runs on Apple Silicon. Fails LOUD when signing IS configured but errors --
# a broken signature must never ship silently again.
#
# Reads from the environment (set by .github/workflows/release.yml):
#   MACOS_SIGN_IDENTITY     Developer ID Application identity (unset -> skip, ad-hoc)
#   NOTARY_KEY_PATH         path to the App Store Connect API .p8
#   MACOS_NOTARY_KEY_ID     API key id
#   MACOS_NOTARY_ISSUER_ID  API issuer id
set -euo pipefail

path="$1"
goos="$2"
goarch="${3:-}"

# Only macOS binaries are signed; every other target is a no-op.
[ "$goos" = "darwin" ] || exit 0

# No identity -> unsigned build (fork / `goreleaser release --snapshot`). Leave the ad-hoc signature
# Go already applied; still runnable, just not Developer-ID/notarized.
if [ -z "${MACOS_SIGN_IDENTITY:-}" ]; then
  echo "macos-sign: MACOS_SIGN_IDENTITY unset -> leaving Go ad-hoc signature on $path" >&2
  exit 0
fi

echo "macos-sign: codesigning $path" >&2
# Hardened runtime (--options runtime) is mandatory for notarization; a plain CLI needs no
# entitlements. --timestamp fetches a secure Apple timestamp (notarization rejects submissions
# without one).
codesign --force --options runtime --timestamp --sign "$MACOS_SIGN_IDENTITY" "$path"
# Fail loud if what we just produced isn't a valid, Developer-ID-rooted signature.
codesign --verify --strict --verbose=2 "$path"

echo "macos-sign: notarizing $path" >&2
# A bare Mach-O can't be stapled (only .app/.pkg/.dmg carry a ticket), so we notarize the cdhash:
# Gatekeeper verifies it online on first run. notarytool wants a container, so zip the binary purely
# as a transport for submission -- the archived/released bytes are the signed binary itself.
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
zip="$tmp/ccpool.zip"
json="$tmp/notary.json"
/usr/bin/ditto -c -k --keepParent "$path" "$zip"

xcrun notarytool submit "$zip" \
  --key "$NOTARY_KEY_PATH" \
  --key-id "$MACOS_NOTARY_KEY_ID" \
  --issuer "$MACOS_NOTARY_ISSUER_ID" \
  --wait --timeout 20m \
  --output-format json >"$json"
cat "$json" >&2

# Don't trust the exit code alone; require the terminal status to be "Accepted" (plutil ships on
# every macOS runner). Anything else -> fail the release loudly.
status="$(plutil -extract status raw -o - "$json")"
if [ "$status" != "Accepted" ]; then
  echo "macos-sign: notarization returned '$status' (not Accepted) for $path" >&2
  exit 1
fi
echo "macos-sign: signed + notarized $path" >&2

# Regression guard: actually launch the signed binary. On the stock macOS runner AMFI validates the
# signature at exec, so a signature it would reject (the v0.1.1 quill bug: SIGKILL before main) fails
# the release RIGHT HERE instead of shipping. Only the native arch can run (no Rosetta assumed), so
# cross-arch binaries are skipped; a missing arch arg also skips.
host_arch="$(uname -m)"
if { [ "$host_arch" = "arm64" ] && [ "$goarch" = "arm64" ]; } ||
   { [ "$host_arch" = "x86_64" ] && [ "$goarch" = "amd64" ]; }; then
  if "$path" version >/dev/null 2>&1; then
    echo "macos-sign: smoke-launch OK ($goarch on $host_arch)" >&2
  else
    rc=$?
    echo "macos-sign: signed binary was killed/failed at launch on $host_arch (rc=$rc); AMFI rejected the signature" >&2
    exit 1
  fi
else
  echo "macos-sign: skipping smoke-launch (${goarch:-no-arch} binary on $host_arch host)" >&2
fi
