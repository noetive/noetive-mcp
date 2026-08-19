# The published product is the npm wrapper; this image exists for running the
# server in a container that speaks MCP over stdio, which is how a hosted agent
# runtime consumes it.
FROM golang:1.25-alpine AS build

ARG VERSION=docker

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /noetive-mcp ./cmd/noetive-mcp

FROM gcr.io/distroless/static-debian12:nonroot

# The server makes outbound TLS calls to semantik.noetive.io, so it needs the
# root store; distroless/static ships one, unlike scratch.
COPY --from=build /noetive-mcp /noetive-mcp

USER nonroot:nonroot
ENTRYPOINT ["/noetive-mcp"]
