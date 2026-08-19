# The npm wrapper is how a person installs this; the image is how a hosted agent
# runtime does, running the server in a container that speaks MCP over stdio.
#
# The build stage is pinned to the *build* platform and cross-compiles to the
# target, rather than being emulated into the target platform. Go cross-compiles
# natively with CGO off, so a linux/arm64 image builds at full speed on an amd64
# runner; under QEMU the same build takes minutes. TARGETOS and TARGETARCH are
# supplied by BuildKit and default to the host, so a plain `docker build` still
# produces an image for the machine that ran it.
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS build

ARG VERSION=docker
ARG TARGETOS
ARG TARGETARCH

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /noetive-mcp ./cmd/noetive-mcp

FROM gcr.io/distroless/static-debian12:nonroot

# The MCP registry's proof that this image belongs to the server it is listed
# under: it pulls the manifest and refuses to publish unless this label matches
# `name` in server.json. Also set as an index annotation at push time — the
# registry resolves the multi-arch index, which does not inherit a child's
# labels.
LABEL io.modelcontextprotocol.server.name="io.noetive/mcp-server"

# The server makes outbound TLS calls to semantik.noetive.io, so it needs the
# root store; distroless/static ships one, unlike scratch.
COPY --from=build /noetive-mcp /noetive-mcp

USER nonroot:nonroot
ENTRYPOINT ["/noetive-mcp"]
