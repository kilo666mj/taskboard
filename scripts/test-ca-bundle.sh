#!/bin/sh
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
tmp_dir=$(mktemp -d)
trap 'rm -rf "$tmp_dir"' 0
trap 'exit 1' HUP INT TERM

openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj /CN=Test-CA \
    -keyout "$tmp_dir/key.pem" -out "$tmp_dir/safe.pem" >/dev/null 2>&1
sh "$root/scripts/check-ca-bundle.sh" "$tmp_dir/safe.pem"

expect_rejected() {
    if sh "$root/scripts/check-ca-bundle.sh" "$1" >"$tmp_dir/output" 2>&1; then
        echo "unexpectedly accepted CA bundle: $1" >&2
        exit 1
    fi
}

# The fixture is the public root from the previous pinned Debian image. The
# verifier must reject it even when concatenated into an otherwise valid bundle.
blocked="$root/testdata/certificates/emsign-ecc-root-g3.pem"
expect_rejected "$blocked"
grep -q 'still trusts blocked emSign' "$tmp_dir/output"
cat "$tmp_dir/safe.pem" "$blocked" >"$tmp_dir/mixed.pem"
expect_rejected "$tmp_dir/mixed.pem"
grep -q 'still trusts blocked emSign' "$tmp_dir/output"

: >"$tmp_dir/empty.pem"
expect_rejected "$tmp_dir/empty.pem"
printf '%s\n' '-----BEGIN CERTIFICATE-----' invalid '-----END CERTIFICATE-----' >"$tmp_dir/invalid.pem"
expect_rejected "$tmp_dir/invalid.pem"
sed '$d' "$tmp_dir/safe.pem" >"$tmp_dir/truncated.pem"
expect_rejected "$tmp_dir/truncated.pem"
cat "$tmp_dir/safe.pem" "$tmp_dir/truncated.pem" >"$tmp_dir/truncated-mixed.pem"
expect_rejected "$tmp_dir/truncated-mixed.pem"
expect_rejected "$tmp_dir/missing.pem"

echo 'CA bundle regression tests passed'
