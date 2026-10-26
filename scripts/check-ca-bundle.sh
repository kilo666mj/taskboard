#!/bin/sh
# Check the bundle copied into the scratch image, independently of CA filenames.
set -eu

bundle=${1:?usage: check-ca-bundle.sh CA_BUNDLE}
# SHA-256 of the DER-encoded emSign ECC Root CA - G3 certificate.
blocked=86A1ECBA089C4A8D3BBE2734C612BA341D813E043CF9E8A862CD5C57A36BBE6B
cert_dir=$(mktemp -d)
trap 'rm -rf "$cert_dir"' 0
trap 'exit 1' HUP INT TERM

# Split PEM certificates and reject empty or truncated bundles. A parse failure
# must not turn into a successful "no blocked fingerprint found" result.
awk -v dir="$cert_dir" '
    /^-----BEGIN CERTIFICATE-----$/ {
        if (inside) exit 1
        count++
        file = dir "/" count ".pem"
        inside = 1
    }
    inside { print > file }
    /^-----END CERTIFICATE-----$/ {
        if (!inside) exit 1
        close(file)
        inside = 0
    }
    END { if (inside || count == 0) exit 1 }
' "$bundle"

for cert in "$cert_dir"/*.pem; do
    fingerprint=$(openssl x509 -in "$cert" -noout -fingerprint -sha256)
    fingerprint=$(printf '%s' "${fingerprint#*=}" | tr -d ':')
    if [ "$fingerprint" = "$blocked" ]; then
        echo 'CA bundle still trusts blocked emSign ECC Root CA - G3' >&2
        exit 1
    fi
done

echo 'CA bundle verified: emSign ECC Root CA - G3 is absent'
