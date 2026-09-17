FROM golang:1.27.1-bookworm AS build
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN mkdir -p /out/data && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X github.com/kilo666mj/taskboard/internal/server.Version=${VERSION}" -o /out/taskboard ./cmd/taskboard

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build /out/taskboard /usr/local/bin/taskboard
COPY --from=build --chown=nonroot:nonroot /out/data /var/lib/taskboard
ENV TASKBOARD_LISTEN_ADDRESS=0.0.0.0:8095 \
    TASKBOARD_DATABASE_PATH=/var/lib/taskboard/taskboard.db
VOLUME ["/var/lib/taskboard"]
EXPOSE 8095
ENTRYPOINT ["/usr/local/bin/taskboard"]
