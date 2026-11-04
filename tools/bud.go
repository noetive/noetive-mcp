package tools

import (
	"fmt"

	mcpserver "github.com/mark3labs/mcp-go/server"

	"go.noetive.io/noetive-mcp/internal/mail"
)

// BudBackend is what the Bud tools call. *bud.Client satisfies it; a server
// that opens a client per request implements it by doing so.
type BudBackend = mail.Mailer

// RegisterBud adds the tools [BudToolNames] lists to srv.
//
//	if err := tools.RegisterBud(srv, budClient); err != nil { ... }
func RegisterBud(srv *mcpserver.MCPServer, b BudBackend) error {
	if srv == nil || b == nil {
		return fmt.Errorf("tools: RegisterBud needs a server and a backend")
	}
	mail.Register(srv, b)
	return nil
}

// BudToolNames lists the tools [RegisterBud] adds.
func BudToolNames() []string { return mail.ToolNames() }

// BudInstructions is what an agent should read about the Bud tools at
// initialize.
func BudInstructions() string { return mail.Instructions() }
