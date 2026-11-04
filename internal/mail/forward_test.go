package mail_test

import (
	"reflect"
	"strings"
	"testing"
	"time"

	json "github.com/goccy/go-json"
	"go.noetive.io/noetive-sdk-go/bud"

	"go.noetive.io/noetive-mcp/internal/mail"
)

// TestEveryArgumentReachesTheService compares what each route sends with what
// the agent asked for, field by field. An argument the schema offers and the
// handler drops is the worst kind of bug here: the call succeeds, and the agent
// believes it filtered, replied-all or sent as someone it did not.
func TestEveryArgumentReachesTheService(t *testing.T) {
	t.Run("folder listing", func(t *testing.T) {
		r := &recorder{}
		call(t, mail.ReadTool, r, `{"id":"ag_01x","folder":"sent","unread":true,"agent":true,"since":"2026-01-01T00:00:00Z",`+
			`"before":"2026-02-01T00:00:00Z","from":"ada@example.com","thread":"thread_01x","limit":10,"cursor":"c1"}`)
		want := bud.In{In: "ag_01x", Folder: "sent", Unread: true, Agent: true, Since: "2026-01-01T00:00:00Z",
			Before: "2026-02-01T00:00:00Z", From: "ada@example.com", Thread: "thread_01x", Limit: 10, Cursor: "c1"}
		if !reflect.DeepEqual(r.in, want) {
			t.Errorf("ListFolder got %+v, want %+v", r.in, want)
		}
	})
	t.Run("correspondents", func(t *testing.T) {
		r := &recorder{}
		call(t, mail.ReadTool, r, `{"id":"ag_01x","q":"ada","limit":5,"cursor":"c2"}`)
		want := bud.In{In: "ag_01x", Q: "ada", Limit: 5, Cursor: "c2"}
		if r.called != "ListCorrespondents" || !reflect.DeepEqual(r.in, want) {
			t.Errorf("%s got %+v, want %+v", r.called, r.in, want)
		}
	})
	t.Run("mailbox", func(t *testing.T) {
		r := &recorder{}
		call(t, mail.ReadTool, r, `{"id":"ag_01x"}`)
		if want := (bud.ByID{ID: "ag_01x"}); !reflect.DeepEqual(r.byID, want) {
			t.Errorf("DescribeMailbox got %+v", r.byID)
		}
	})
	t.Run("thread", func(t *testing.T) {
		r := &recorder{}
		call(t, mail.ReadTool, r, `{"id":"thread_01x","max_messages":7,"cursor":"c3"}`)
		if want := (bud.ByID{ID: "thread_01x", MaxMessages: 7, Cursor: "c3"}); !reflect.DeepEqual(r.byID, want) {
			t.Errorf("DescribeThread got %+v", r.byID)
		}
	})
	t.Run("message", func(t *testing.T) {
		r := &recorder{}
		call(t, mail.ReadTool, r, `{"id":"message_01x","render":"raw","max_chars":9,"cursor":"c4"}`)
		if want := (bud.ByID{ID: "message_01x", Render: "raw", MaxChars: 9, Cursor: "c4"}); !reflect.DeepEqual(r.byID, want) {
			t.Errorf("DescribeMessage got %+v", r.byID)
		}
	})
	t.Run("wait", func(t *testing.T) {
		r := &recorder{}
		call(t, mail.WaitTool, r, `{"mailbox":"ag_01x","cursor":"9","timeout_s":1,"types":["mail.received","mail.held"],"agent_part":true}`)
		yes := true
		want := bud.WaitInput{Mailbox: "ag_01x", Cursor: "9", TimeoutSeconds: 1, Types: []string{"mail.received", "mail.held"}, AgentPart: &yes}
		if !reflect.DeepEqual(r.wait, want) {
			t.Errorf("Wait got %+v, want %+v", r.wait, want)
		}
	})
	t.Run("send", func(t *testing.T) {
		r := &recorder{sent: bud.SendOutput{State: bud.StateQueued}}
		call(t, mail.SendTool, r, `{"to":["a@example.com"],"cc":["b@example.com"],"bcc":["c@example.com"],"subject":"s","text":"t","html":"<p>h</p>",`+
			`"in_reply_to":"message_00x","reply_all":true,"from":"me@agents.example.com","on_behalf_of":"ag_02x","idempotency_key":"k"}`)
		want := bud.SendInput{To: []string{"a@example.com"}, CC: []string{"b@example.com"}, BCC: []string{"c@example.com"},
			Subject: "s", Text: "t", HTML: "<p>h</p>", InReplyTo: "message_00x", ReplyAll: true,
			From: "me@agents.example.com", OnBehalfOf: "ag_02x", IdempotencyKey: "k"}
		if !reflect.DeepEqual(r.send, want) {
			t.Errorf("Send got %+v, want %+v", r.send, want)
		}
	})
}

// TestAnyListingArgumentListsTheFolder: each listing filter on its own turns a
// mailbox read into a listing. Without that, a filter the agent set alone would
// return the mailbox's description and look like a filter that matched nothing.
func TestAnyListingArgumentListsTheFolder(t *testing.T) {
	for _, arg := range []string{`"folder":"inbox"`, `"unread":true`, `"agent":true`, `"since":"2026-01-01T00:00:00Z"`,
		`"before":"2026-01-01T00:00:00Z"`, `"from":"a@example.com"`, `"thread":"thread_01x"`, `"limit":3`, `"cursor":"c"`} {
		r := &recorder{}
		call(t, mail.ReadTool, r, `{"id":"ag_01x",`+arg+`}`)
		if r.called != "ListFolder" {
			t.Errorf("%s alone reached %s", arg, r.called)
		}
	}
}

// TestEveryCallIsBounded: a tool call blocks the agent's turn, so one without a
// deadline reads to the user as a hung editor.
func TestEveryCallIsBounded(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(*recorder)
	}{
		{"read", func(r *recorder) { call(t, mail.ReadTool, r, `{"id":"message_01x"}`) }},
		{"catalog", func(r *recorder) { call(t, mail.ReadTool, r, `{"id":"catalog"}`) }},
		{"wait", func(r *recorder) { call(t, mail.WaitTool, r, `{}`) }},
		{"put", func(r *recorder) { call(t, mail.PutTool, r, `{"id":"message_01x","changes":{"read":true}}`) }},
		{"send", func(r *recorder) { call(t, mail.SendTool, r, `{"to":["a@example.com"],"idempotency_key":"k"}`) }},
	} {
		r := &recorder{}
		tc.run(r)
		if r.called == "" || !r.deadline {
			t.Errorf("%s reached %q with no deadline", tc.name, r.called)
		}
	}
}

// TestAWaitWindowIsBoundedBothWays: one second is the shortest window and the
// service's cap the longest; anything outside is refused before sending.
func TestAWaitWindowIsBoundedBothWays(t *testing.T) {
	for args, sent := range map[string]bool{
		`{"timeout_s":1}`:  true,
		`{"timeout_s":25}`: true,
		`{"timeout_s":0}`:  false,
		`{"timeout_s":26}`: false,
	} {
		r := &recorder{}
		call(t, mail.WaitTool, r, args)
		if (r.called != "") != sent {
			t.Errorf("%s: sent %v, want %v", args, r.called != "", sent)
		}
	}
}

// TestAnEventLineSaysWhatHappenedAndWhere: the text is what an agent without
// structured content reads, so it must name the message to open, the sender and
// whether it is aligned, and the cursor to wait from next.
func TestAnEventLineSaysWhatHappenedAndWhere(t *testing.T) {
	r := &recorder{waited: bud.WaitOutput{Cursor: "77", Events: []bud.JournalEvent{
		{Type: bud.EventReceived, Mailbox: "ag_01x", Message: "message_01x", Data: bud.EventData{From: "ada@example.com", Aligned: true, Subject: "Order"}},
		{Type: bud.EventRead, Mailbox: "ag_01x"},
	}}}
	result := call(t, mail.WaitTool, r, `{}`)
	got := text(result)
	for _, want := range []string{"- mail.received mailbox=ag_01x message=message_01x", `from="ada@example.com" aligned=true`, `subject="Order"`, `cursor "77"`} {
		if !strings.Contains(got, want) {
			t.Errorf("event text lacks %q:\n%s", want, got)
		}
	}
	if line := strings.Split(got, "\n")[2]; line != "- mail.read mailbox=ag_01x" {
		t.Errorf("an event with no message or sender renders as %q", line)
	}
	structured, _ := json.Marshal(result.StructuredContent)
	if !strings.Contains(string(structured), `"message_01x"`) || !strings.Contains(string(structured), `"empty":false`) {
		t.Errorf("structured events are %s", structured)
	}
}

// TestAHeldSendWithoutAnApprovalYetIsStillHeld: the approval is filed in the
// background, so a held result can come back before it has an id. It is held
// all the same, and the warning must not depend on the id being there.
func TestAHeldSendWithoutAnApprovalYetIsStillHeld(t *testing.T) {
	r := &recorder{sent: bud.SendOutput{Message: "message_01x", State: bud.StateHeld}}
	got := text(call(t, mail.SendTool, r, `{"to":["a@example.com"],"idempotency_key":"k"}`))
	if !strings.Contains(got, "waits on an approval") || !strings.Contains(got, "Do not send it again") {
		t.Errorf("held send without an approval id renders as %q", got)
	}
}

// TestTheCatalogNamesItsOperations: an agent reads the catalog to learn what
// exists, so the text has to list the operations, not only say it was read.
func TestTheCatalogNamesItsOperations(t *testing.T) {
	r := &recorder{catalog: bud.Catalog{Contract: "bud/v1", Operations: []bud.CatalogOperation{{Name: "folder.list"}, {Name: "send"}}}}
	if got := text(call(t, mail.ReadTool, r, `{"id":"catalog"}`)); got != "Bud serves bud/v1: folder.list send" {
		t.Errorf("catalog text is %q", got)
	}
}

// TestAReadWithNoRenderingSaysWhereItIs: some reads carry only structured
// content; the text must still say what was read rather than come back empty,
// which an agent reads as nothing there.
func TestAReadWithNoRenderingSaysWhereItIs(t *testing.T) {
	r := &recorder{read: bud.ReadOutput{Ref: "ag_01x", Kind: "mailbox"}}
	if got := text(call(t, mail.ReadTool, r, `{"id":"ag_01x"}`)); !strings.Contains(got, "mailbox ag_01x") {
		t.Errorf("an unrendered read says %q", got)
	}
}

// TestALongIdIsCutInTheRefusal: an id may have been copied out of a message, so
// a refusal quotes a bounded prefix of it rather than echoing a stranger's text
// whole into what the agent reads as the server speaking.
func TestALongIdIsCutInTheRefusal(t *testing.T) {
	long := "x" + strings.Repeat("y", 200)
	got := text(call(t, mail.ReadTool, &recorder{}, `{"id":"`+long+`"}`))
	if strings.Contains(got, long) || !strings.Contains(got, strings.Repeat("y", 63)+"…") {
		t.Errorf("a long id is quoted as %q", got)
	}
}

// TestAnAbsentArgumentIsLeftToTheService: defaults belong to the service, which
// documents them. A tool that filled one in, a page of one or a reply to
// everyone, would change what the call means without the agent asking.
func TestAnAbsentArgumentIsLeftToTheService(t *testing.T) {
	r := &recorder{}
	call(t, mail.ReadTool, r, `{"id":"message_01x"}`)
	if want := (bud.ByID{ID: "message_01x"}); !reflect.DeepEqual(r.byID, want) {
		t.Errorf("a bare message read sent %+v", r.byID)
	}
	r = &recorder{}
	call(t, mail.ReadTool, r, `{"id":"thread_01x"}`)
	if want := (bud.ByID{ID: "thread_01x"}); !reflect.DeepEqual(r.byID, want) {
		t.Errorf("a bare thread read sent %+v", r.byID)
	}
	r = &recorder{}
	call(t, mail.ReadTool, r, `{"id":"ag_01x","limit":1}`)
	if r.called != "ListFolder" || r.in.Limit != 1 {
		t.Errorf("a page of one reached %s with %+v", r.called, r.in)
	}
	r = &recorder{sent: bud.SendOutput{State: bud.StateQueued}}
	call(t, mail.SendTool, r, `{"to":["a@example.com"],"idempotency_key":"k"}`)
	if want := (bud.SendInput{To: []string{"a@example.com"}, IdempotencyKey: "k"}); !reflect.DeepEqual(r.send, want) {
		t.Errorf("a bare send sent %+v", r.send)
	}
	r = &recorder{sent: bud.SendOutput{State: bud.StateQueued}}
	call(t, mail.SendTool, r, `{"to":["a@example.com"],"agent":null,"idempotency_key":"k"}`)
	if r.called != "Send" || r.send.Agent != nil {
		t.Errorf("a null agent part reached %q as %s", r.called, r.send.Agent)
	}
}

// TestAWaitIsBoundedByItsWindow: the deadline is the window plus the stream's
// opening, so a wait is neither cut off inside its window nor left to hang well
// past it.
func TestAWaitIsBoundedByItsWindow(t *testing.T) {
	r := &recorder{}
	start := time.Now()
	call(t, mail.WaitTool, r, `{"timeout_s":10}`)
	left := r.due.Sub(start)
	if left < 10*time.Second || left > 30*time.Second {
		t.Errorf("a 10 s wait was given %s", left)
	}
	r = &recorder{}
	start = time.Now()
	call(t, mail.ReadTool, r, `{"id":"me"}`)
	if left := r.due.Sub(start); left < 10*time.Second || left > time.Minute {
		t.Errorf("a read was given %s", left)
	}
}

// TestARepeatedSendSaysWhereTheFirstGot: a repeat under a used key reports the
// first message's state now, and a failed one says why.
func TestARepeatedSendSaysWhereTheFirstGot(t *testing.T) {
	r := &recorder{sent: bud.SendOutput{Message: "message_01x", State: bud.StateFailed, Reason: "the domain is not verified"}}
	got := text(call(t, mail.SendTool, r, `{"to":["a@example.com"],"idempotency_key":"k"}`))
	if got != "Message message_01x is failed. the domain is not verified" {
		t.Errorf("a repeated failed send renders as %q", got)
	}
	r = &recorder{sent: bud.SendOutput{Message: "message_01x", State: bud.StateDelivered}}
	if got := text(call(t, mail.SendTool, r, `{"to":["a@example.com"],"idempotency_key":"k"}`)); got != "Message message_01x is delivered." {
		t.Errorf("a repeated delivered send renders as %q", got)
	}
}
