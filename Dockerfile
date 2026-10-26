FROM golang:1.27.3-bookworm@sha256:972b4110ba0f47539a975c31e52c64765e1ff19f2d74469ae4a595c6676e80a5 AS build
ARG VERSION=dev
# Older base images still include emSign ECC Root CA - G3. Disable it when
# present, and verify the actual bundle even when Debian has removed it.
COPY scripts/check-ca-bundle.sh /tmp/check-ca-bundle.sh
RUN sed -i 's|^mozilla/emSign_ECC_Root_CA_-_G3\.crt$|!&|' /etc/ca-certificates.conf && \
    update-ca-certificates --fresh >/dev/null && \
    sh /tmp/check-ca-bundle.sh /etc/ssl/certs/ca-certificates.crt
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN mkdir -p /out/data && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X github.com/kilo666mj/taskboard/internal/server.Version=${VERSION}" -o /out/taskboard ./cmd/taskboard

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/taskboard /usr/local/bin/taskboard
COPY --from=build --chown=65532:65532 /out/data /var/lib/taskboard
ENV SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt \
    TASKBOARD_LISTEN_ADDRESS=0.0.0.0:8095 \
    TASKBOARD_DATABASE_PATH=/var/lib/taskboard/taskboard.db
VOLUME ["/var/lib/taskboard"]
EXPOSE 8095
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/taskboard"]
