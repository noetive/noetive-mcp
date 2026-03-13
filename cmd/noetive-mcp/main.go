package main

import (
	"log"
	"os"

	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/noetive/noetive-mcp/internal/server"
)

var version = "dev"

func main() {
	log.SetOutput(os.Stderr)

	s := server.New(version, "https://www.noetive.io/health")
	if err := mcpserver.ServeStdio(s); err != nil {
		log.Fatal(err)
	}
}
