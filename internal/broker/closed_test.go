package broker_test

import (
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/noetive/noetive-mcp/internal/broker"
	"github.com/noetive/noetive-mcp/internal/targeting"
)

// Every tool that routes has to honour a closed namespace. The check lives in
// one place, but "lives in one place" is a claim about wiring, and a tool built
// without its policy compiles and passes every other test in this package while
// publishing straight into the namespace an operator closed.
func TestNoRoutingToolReachesAClosedNamespace(t *testing.T) {
	scenarios := []struct {
		name    string
		build   func(*stubBroker) (mcp.Tool, mcpserver.ToolHandlerFunc)
		args    map[string]any
		reached func(*stubBroker) bool
	}{
		{
			name:    "publish",
			build:   func(s *stubBroker) (mcp.Tool, mcpserver.ToolHandlerFunc) { return broker.PublishTool(s, closedGlobal) },
			args:    map[string]any{"text": "an incident nobody outside this tenant should read"},
			reached: func(s *stubBroker) bool { return len(s.publishReq.Items) > 0 },
		},
		{
			name:    "search",
			build:   func(s *stubBroker) (mcp.Tool, mcpserver.ToolHandlerFunc) { return broker.SearchTool(s, closedGlobal) },
			args:    map[string]any{"query": `MATCH DISTANCE("payment reconciliation") WITHIN 0.4`},
			reached: func(s *stubBroker) bool { return s.searchReq.Query != "" },
		},
		{
			name: "subscribe",
			build: func(s *stubBroker) (mcp.Tool, mcpserver.ToolHandlerFunc) {
				return broker.SubscribeTool(s, closedGlobal)
			},
			args:    map[string]any{"query": `MATCH DISTANCE("payment reconciliation") WITHIN 0.4`, "max_matches": float64(1), "wait_seconds": float64(60)},
			reached: func(s *stubBroker) bool { return s.subReq.Query != "" },
		},
	}

	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			stub := &stubBroker{}
			_, handler := sc.build(stub)

			args := map[string]any{"namespace": targeting.GlobalNamespace}
			for k, v := range sc.args {
				args[k] = v
			}

			message := requireError(t, call(t, handler, args))

			if !strings.Contains(message, targeting.GlobalNamespace) {
				t.Errorf("expected the refusal to name the namespace, got: %s", message)
			}
			if sc.reached(stub) {
				t.Fatal("the broker was called despite a closed namespace")
			}
		})
	}
}

// Closing the shared namespace must not close the server. Every other
// destination keeps working, or the switch is an outage rather than a boundary.
func TestAClosedGlobalNamespaceLeavesOtherRoutingIntact(t *testing.T) {
	stub := &stubBroker{}
	_, handler := broker.PublishTool(stub, closedGlobal)

	call(t, handler, map[string]any{
		"text":      "the gateway returns 202 before the write lands",
		"namespace": "payments",
	})

	if stub.publishReq.Namespace != "payments" {
		t.Errorf("expected the call to reach payments, got %q", stub.publishReq.Namespace)
	}
}

// The tool description is the strongest suggestion an agent gets about what to
// pass. Leaving "global" in it on a server that refuses "global" spends the
// agent's first attempt on a destination it cannot use, every time.
func TestTheNamespaceDescriptionStopsAdvertisingAClosedNamespace(t *testing.T) {
	for _, sc := range []struct {
		name      string
		policy    targeting.Policy
		advertise bool
	}{
		{"open", configured, true},
		{"closed", closedGlobal, false},
	} {
		t.Run(sc.name, func(t *testing.T) {
			tool, _ := broker.PublishTool(&stubBroker{}, sc.policy)

			description := namespaceDescription(t, tool)
			mentions := strings.Contains(description, `"`+targeting.GlobalNamespace+`"`)

			if sc.advertise && !mentions {
				t.Errorf("expected the shared namespace to be offered as an example, got: %s", description)
			}
			if !sc.advertise {
				if strings.Contains(description, "for example") {
					t.Errorf("expected no example namespace when the shared one is closed, got: %s", description)
				}
				if !mentions {
					t.Errorf("expected the description to say the shared namespace is closed, got: %s", description)
				}
			}
		})
	}
}

// namespaceDescription reads the description of a tool's namespace argument,
// which is what the agent actually sees in the schema.
func namespaceDescription(t *testing.T, tool mcp.Tool) string {
	t.Helper()

	property, ok := tool.InputSchema.Properties["namespace"].(map[string]any)
	if !ok {
		t.Fatalf("expected a namespace property, got %#v", tool.InputSchema.Properties["namespace"])
	}
	description, ok := property["description"].(string)
	if !ok {
		t.Fatalf("expected the namespace property to carry a description, got %#v", property)
	}
	return description
}
