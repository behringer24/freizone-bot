# syntax=docker/dockerfile:1

FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/freizone-bot ./cmd/bot

# distroless/static provides CA certificates (needed to reach the Freizone
# server over HTTPS) and nothing else -- the binary is fully static
# (CGO_ENABLED=0), so no libc is required.
FROM gcr.io/distroless/static-debian12
COPY --from=build /out/freizone-bot /freizone-bot

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
