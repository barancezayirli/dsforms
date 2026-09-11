# Build stage
FROM golang:1.25-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .

# Stamped into main.version and shown in the admin sidebar. main.go has
# documented this since the badge was added, but nothing passed it — so every
# published release rendered the "dev" fallback, including tagged ones. The
# default keeps a plain `docker build` honest about being an untagged build.
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -ldflags="-s -w -X main.version=${VERSION}" -o dsforms .

# Runtime stage
FROM alpine:3.19
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=builder /app/dsforms .
VOLUME ["/data"]
EXPOSE 8080

# The endpoint asks the database, not the HTTP server, so this detects the state
# where the process is listening and every route 500s — which a failed restore
# could produce, and which only a restart clears. Without a HEALTHCHECK the
# endpoint is a route nobody calls: `restart: unless-stopped` does not restart a
# container whose process is alive.
#
# In the image rather than only in compose, so it applies however the image is
# deployed. wget is in busybox on alpine, so nothing extra is installed.
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD wget -qO- http://localhost:8080/healthz || exit 1

CMD ["./dsforms"]
