# syntax=docker/dockerfile:1

FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/freizone-bot ./cmd/bot

# An empty state directory to carry into the final image. A named volume takes
# its owner and mode from the directory that is already there, so without this
# Docker creates /data as root at run time and the nonroot process cannot write
# its own account into it -- which is what the documented `docker run -v` did.
RUN mkdir -p /out/data

# distroless/static provides CA certificates (needed to reach the Freizone
# server over HTTPS) and nothing else -- the binary is fully static
# (CGO_ENABLED=0), so no libc is required.
FROM gcr.io/distroless/static-debian12
COPY --from=build /out/freizone-bot /freizone-bot

# 65532 is distroless's nonroot, numerically rather than by name so this does not
# depend on the base image carrying an /etc/passwd entry.
COPY --from=build --chown=65532:65532 /out/data /data

ENV FREIZONE_BOT_STATE_DIR=/data
VOLUME ["/data"]

# Deliberately no EXPOSE: the bot opens no network listener. Its only ingress
# is a unix socket inside the state directory, which is why `docker exec
# <container> /freizone-bot send ...` works and nothing has to be published.
# When the webhook receiver lands (BOT-08) this line comes back, and its
# absence until then is the documentation of that property.

# This container holds long-lived private keys, unlike a stateless relay.
USER nonroot:nonroot

ENTRYPOINT ["/freizone-bot"]
