# syntax=docker/dockerfile:1
FROM golang:1.26-bookworm AS build
ARG TARGETARCH
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
RUN test "$TARGETARCH" = amd64 && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o /out/cfip-ddns ./cmd/cfip-ddns

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --system --uid 10001 --home-dir /app --shell /usr/sbin/nologin cfip \
    && mkdir -p /app/data && chown cfip:cfip /app /app/data
WORKDIR /app
COPY --from=build /out/cfip-ddns /app/cfip-ddns
COPY --chmod=755 cfst /app/cfst
COPY ip.txt /app/ip.txt
COPY third_party /app/third_party
USER 10001:10001
ENV DATA_DIR=/app/data
VOLUME ["/app/data"]
STOPSIGNAL SIGTERM
ENTRYPOINT ["/app/cfip-ddns"]
