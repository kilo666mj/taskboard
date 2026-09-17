#!/usr/bin/env bash
set -euo pipefail

version="${1:-dev}"
output_dir="${2:-dist}"

case "$version" in
    *[!A-Za-z0-9._+-]*)
        echo "error: version contains unsupported characters" >&2
        exit 1
        ;;
esac

mkdir -p "$output_dir"
output_dir="$(cd "$output_dir" && pwd)"

for platform in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do
    os="${platform%/*}"
    arch="${platform#*/}"
    bundle="taskboard-${version}-${os}-${arch}"
    staging="${output_dir}/${bundle}"

    mkdir -p "${staging}/bin"
    CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath \
        -ldflags "-s -w -X github.com/kilo666mj/taskboard/internal/server.Version=${version}" \
        -o "${staging}/bin/taskboard" ./cmd/taskboard
    CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath -ldflags "-s -w" \
        -o "${staging}/bin/taskboard-init-config" ./cmd/taskboard-init-config
    CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath -ldflags "-s -w" \
        -o "${staging}/bin/taskboard-keygen" ./cmd/taskboard-keygen
    cp LICENSE README.md "${staging}/"
    tar -C "$output_dir" -czf "${output_dir}/${bundle}.tar.gz" "$bundle"
    rm -rf "$staging"
done

(cd "$output_dir" && sha256sum ./*.tar.gz > SHA256SUMS)
