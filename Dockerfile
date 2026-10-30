# syntax=docker/dockerfile:1
# Static binary on distroless, linux/amd64 only (DS220+, Intel J4025). Templates and
# static assets live in internal/web and are embedded with go:embed, so the COPY of
# internal/ is enough. Assets placed outside cmd/ and internal/ need a COPY here and an
# allow-list line in .dockerignore.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
ARG TARGETOS=linux
ARG TARGETARCH=amd64
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/nas-files ./cmd/nas-files \
 && mkdir /out/data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/nas-files /nas-files
# An empty data directory, so "no Mounts" is reported rather than "no such directory".
COPY --from=build /out/data /data
USER 65532:65532
EXPOSE 8080
# Distroless has no shell or wget, so the binary probes itself.
HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=3 CMD ["/nas-files", "-healthcheck"]
ENTRYPOINT ["/nas-files"]
