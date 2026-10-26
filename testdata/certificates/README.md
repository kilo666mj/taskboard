# CA bundle regression fixture

`emsign-ecc-root-g3.pem` is the public emSign ECC Root CA - G3 certificate,
extracted from `/usr/share/ca-certificates/mozilla/emSign_ECC_Root_CA_-_G3.crt`
in the previous pinned build image:

```text
golang:1.27.3-bookworm@sha256:5c3e9b43cfd3bb95c7937462e29d87ada57b481a96fa23ad0eb61778d7a00110
SHA-256: 86:A1:EC:BA:08:9C:4A:8D:3B:BE:27:34:C6:12:BA:34:1D:81:3E:04:3C:F9:E8:A8:62:CD:5C:57:A3:6B:BE:6B
```

It is a negative test fixture, never a trusted CA. No private key is included.
Run `sh scripts/test-ca-bundle.sh` to check rejection of this certificate and
empty, malformed, or truncated bundles.
