# Builder stage: compile envreplace as a fully static binary.
FROM golang:1.23-alpine@sha256:383395b794dffa5b53012a212365d40c8e37109a626ca30d6151c8348d380b5f AS builder

WORKDIR /src

# The golang image ships only the toolchain; the external linker that
# produces the fully static binary needs a C compiler.
RUN apk add --no-cache gcc musl-dev

# Copy only what the build needs (see .dockerignore).
COPY . .

# Link with the host C compiler and produce a fully static binary
# (-linkmode=external -extldflags=-static). The executable is
# self-contained: the Go runtime is statically embedded and there are
# no shared C library dependencies (equivalent of CGO_ENABLED=0; this
# project uses no CGO at all). -buildvcs=false keeps the build
# independent of the repository's Git state.
RUN go build \
    -ldflags='-linkmode=external -extldflags=-static' \
    -buildvcs=false \
    ./... \
    && test -x /src/envreplace

# Runtime stage: copy the static binary into a scorched-earth image.
# The image runs as the non-root user "nonroot" with uid/gid 65532 (not
# to be confused with the traditional `nobody` uid 65534), so mounted
# input/output directories must be readable and writable by that user.
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=builder /src/envreplace /usr/local/bin/envreplace
ENTRYPOINT ["/usr/local/bin/envreplace"]