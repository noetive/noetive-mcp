FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -ldflags "-X main.version=$(cat VERSION 2>/dev/null || echo docker)" -o /noetive-mcp ./cmd/noetive-mcp

FROM alpine:latest
COPY --from=build /noetive-mcp /noetive-mcp
ENTRYPOINT ["/noetive-mcp"]
