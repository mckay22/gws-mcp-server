# Build a fully static binary, then ship it on scratch.
FROM golang:1.25 AS build

WORKDIR /src

# Cache module downloads.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
# CGO off → static, pure-Go binary that runs on scratch.
ARG VERSION=docker
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath \
    -ldflags="-s -w -X main.version=${VERSION}" -o /out/gws-mcp-server .

# Runtime: scratch (nothing but the binary and CA certs).
FROM scratch

# CA certificates so the server can reach Google's APIs over HTTPS.
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/gws-mcp-server /gws-mcp-server

# Run unprivileged (scratch has no users; use the conventional "nobody" uid).
USER 65534:65534

# Default transport is stdio (classic-delegated). Configuration is via GWS_*
# environment variables at runtime. Classic-delegated sign-in opens a loopback
# browser flow and keeps the token in memory only — awkward in a container, so
# the image is intended for resource-server mode (--http), which authenticates
# each request against an OIDC issuer and impersonates via a domain-wide-
# delegation service account whose key is mounted at runtime (GWS_DWD_SA_KEY).
# See docs/quickstart.md and docs/auth.md.
ENTRYPOINT ["/gws-mcp-server"]
