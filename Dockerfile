# Builds cnimbus itself, then wraps it in a minimal runtime image so
# `cnimbus prepare` can also run entirely inside Docker -- the runtime
# stage carries the `docker` CLI (not a daemon) so it can drive
# `prepare`'s own container builds through a mounted host socket.
# See BUILD.md for the full "Docker-only, nothing else installed" flow.

# syntax=docker/dockerfile:1
FROM golang:1.26.5 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS=linux
ARG TARGETARCH=amd64
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/cnimbus ./cmd/cnimbus

FROM docker:29-cli AS runtime
COPY --from=build /out/cnimbus /usr/local/bin/cnimbus
WORKDIR /work
ENTRYPOINT ["cnimbus"]
