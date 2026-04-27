# Dropzone as a base image.
#
# Use this as a FROM in your own Dockerfile to install signed CLI tools
# at build time and ship the resulting image without dropzone itself
# in the way.
#
# Example:
#
#   FROM ghcr.io/uhryniuk/dropzone:latest
#   RUN dz install jq
#   RUN dz install ripgrep
#   ENTRYPOINT ["jq"]
#
# Anything dz install drops at /root/.dropzone/bin/<name> ends up on
# PATH in the final image, so the entrypoint can reference the
# installed binaries by name. The shimmed binaries run against their
# bundled rootfs under /root/.dropzone/packages/, so removing dropzone
# itself from a downstream image does not break them; they only need
# /bin/sh and the bundled rootfs to exist.

# ---- Build stage ----
# Static Go binary so the runtime image can be as minimal as we want.
FROM golang:1.25-alpine AS build

WORKDIR /src

# Pull dependencies first so Docker can cache the layer when only
# source files change.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO off so the binary is fully static and can run on a scratch-like
# base. The trimpath / -s -w combo strips paths and debug info to
# keep the binary small.
RUN CGO_ENABLED=0 go build \
        -trimpath \
        -ldflags="-s -w" \
        -o /out/dz \
        ./cmd/dropzone

# ---- Runtime stage ----
# Alpine because dropzone's wrapper scripts need /bin/sh, and Alpine
# is small, predictable, and freely pullable. ca-certificates is
# required so sigstore-go can verify against the public-good TUF root
# during signature verification at install time.
FROM alpine:3.20

RUN apk add --no-cache ca-certificates

# Drop the static binary in a directory on PATH.
COPY --from=build /out/dz /usr/local/bin/dz

# Make sure subsequent RUN dz install steps land in a stable location
# regardless of which user is active. We default to root for build-
# time installs; downstream images can re-run them as a non-root user
# by setting USER and re-running dz install from that user's HOME.
ENV HOME=/root \
    PATH=/root/.dropzone/bin:/usr/local/bin:/usr/bin:/bin

# Pre-create the dropzone directory tree so first-run logs are quiet
# inside docker build output. Equivalent to what dz does on its own
# the first time it runs; doing it at image build time keeps build
# output focused on the installs the downstream Dockerfile actually
# performs.
RUN mkdir -p /root/.dropzone/bin /root/.dropzone/packages /root/.dropzone/config

# Default command is dz itself so `docker run dropzone` prints help.
ENTRYPOINT ["dz"]
CMD ["--help"]
