package main

import (
	"context"
	"strings"
	"testing"
	"time"

	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	logwire "github.com/openabstractions/abstraction-logging/go/abstraction/logging"
	logclient "github.com/openabstractions/abstraction-logging/go/client"
)

type messageHistoryFake struct {
	page    logclient.ApplicationLogPage
	cursors []string
}

func (f *messageHistoryFake) ReadContext(_ context.Context, target logclient.ApplicationLogTarget, cursor string, records, bytes int64) (logclient.ApplicationLogPage, error) {
	if target.Selection != logwire.ApplicationLogSelectionOneApp || target.Program != "native-runtime" || records != 64 || bytes != 65536 {
		panic("changed attributed history selection")
	}
	f.cursors = append(f.cursors, cursor)
	return f.page, nil
}

type messagePersonFake struct {
	page    msg.Page
	ids     []string
	inboxes msg.InboxPage
}

func (f *messagePersonFake) InboxesForPerson(string) (msg.InboxPage, error) { return f.inboxes, nil }

func (f *messagePersonFake) ReadForPerson(id string) (msg.Page, error) {
	f.ids = append(f.ids, id)
	return f.page, nil
}

func (f *messagePersonFake) StopForPerson(id string) (msg.StopResult, error) {
	f.ids = append(f.ids, "stop:"+id)
	return msg.StopResult{Outcome: msg.OutcomePage, Reason: "stopped"}, nil
}

func TestActivityMessagesOnlyAttributedMetadataAndExplicitBody(t *testing.T) {
	history := &messageHistoryFake{page: logclient.ApplicationLogPage{Outcome: logwire.HistoryPageOutcomePage, Next: "owner-next", AtEnd: true, Records: []logwire.Record{
		{Msg: "Application message", Attrs: map[string]string{"oa_message": "1", "message_id": "sender-1", "from": "sender\u202ex", "to": "app:receiver", "to_context": "target", "kind": "question", "outcome": "sent", "accepted_at": "1000", "expires_at": "2000", "subject": "NEVER_COPY_SUBJECT", "body": "NEVER_COPY_BODY"}},
		{Msg: "Other event", Attrs: map[string]string{"message_id": "unrelated"}},
	}}}
	expires := time.Now().Add(time.Minute).UnixMilli()
	person := &messagePersonFake{page: msg.Page{Outcome: msg.OutcomePage, Headers: []msg.Header{}, Messages: []msg.Message{{Header: msg.Header{MessageID: "sender-1", Subject: "<script>quoted</script>", ExpiresUnixMs: expires}, Body: "exact\né 9007199254740993"}}}}
	oldHistory, oldPerson := messageActivityHistory, messagePersonOperator
	t.Cleanup(func() { messageActivityHistory = oldHistory; messagePersonOperator = oldPerson })
	messageActivityHistory = func(context.Context) (messageHistoryReader, logclient.ApplicationLogTarget, error) {
		program := "native-runtime"
		return history, logclient.ApplicationLogTarget{Selection: logwire.ApplicationLogSelectionOneApp, Program: program}, nil
	}
	messagePersonOperator = func(context.Context) (messagePersonReader, error) { return person, nil }
	panel := (&servicePanel{}).handler("test-key")
	r := panelRequest(t, panel, "/activity/messages?cursor=earlier", nil)
	page := decodePanel[activityMessagesPage](t, r.Code, r.Body.Bytes())
	if page.Outcome != "page" || len(page.Events) != 1 || page.Events[0].From != "Unidentified app" || page.Next != "owner-next" || len(person.ids) != 0 {
		t.Fatal(page, person.ids)
	}
	if strings.Contains(r.Body.String(), "NEVER_COPY") || history.cursors[0] != "earlier" {
		t.Fatal(r.Body.String(), history.cursors)
	}
	if page.Events[0].To != "Unidentified app" || page.Events[0].Technical["from"] != "sender\u202ex" || page.Events[0].Technical["to"] != "app:receiver" {
		t.Fatal("unidentified parties retain their technical evidence", page.Events)
	}
	history.page.Records[0].Attrs["delivery_state"] = "undelivered"
	history.page.Records[0].Attrs["undelivered_reason"] = "expired\u202e"
	history.page.Records[0].Attrs["delivery_at"] = "1500"
	r = panelRequest(t, panel, "/activity/messages", nil)
	deliveryPage := decodePanel[activityMessagesPage](t, r.Code, r.Body.Bytes())
	if deliveryPage.Events[0].Outcome != "sent" || deliveryPage.Events[0].DeliveryState != "undelivered" || deliveryPage.Events[0].UndeliveredReason != "expired" || deliveryPage.Events[0].DeliveryAt != 1500 || len(person.ids) != 0 {
		t.Fatal("delivery facts changed acceptance or fetched body", deliveryPage, person.ids)
	}
	history.page.Records[0].Attrs["metadata_only"] = "1"
	history.page.Records[0].Attrs["undelivered_reason"] = "unconfirmed"
	r = panelRequest(t, panel, "/activity/messages", nil)
	forwardPage := decodePanel[activityMessagesPage](t, r.Code, r.Body.Bytes())
	if !forwardPage.Events[0].MetadataOnly || forwardPage.Events[0].UndeliveredReason != "unconfirmed" || len(person.ids) != 0 {
		t.Fatal("forward metadata offered body or invented delivery", forwardPage, person.ids)
	}
	apps, namedPanel := appPagePanel(t, false)
	history.page.Records[0].Attrs["from_program"] = apps.entries[0].Descriptor.Program
	history.page.Records[0].Attrs["to_program"] = apps.entries[0].Descriptor.Program
	person.inboxes = msg.InboxPage{Outcome: msg.OutcomePage, Inboxes: []msg.Inbox{{Program: "app:" + apps.entries[0].Descriptor.Name, Context: "target", ChatTitle: "Review\u202e\n plan", LastSeenUnixMs: 1000}}}
	r = panelRequest(t, namedPanel.handler("test-key"), "/activity/messages", nil)
	named := decodePanel[activityMessagesPage](t, r.Code, r.Body.Bytes())
	if named.Events[0].From != "Example <b>" || named.Events[0].To != "Example <b>" {
		t.Fatal("current program metadata names the parties", named)
	}
	if named.Events[0].ToChat != "Review plan" || named.Events[0].FromChat != "Unnamed chat" {
		t.Fatal("typed operator metadata did not name exact chat", named.Events)
	}
	person.inboxes.Inboxes[0].ChatTitle = ""
	r = panelRequest(t, namedPanel.handler("test-key"), "/activity/messages", nil)
	lastSeen := decodePanel[activityMessagesPage](t, r.Code, r.Body.Bytes())
	if !strings.HasPrefix(lastSeen.Events[0].ToChat, "Chat last seen ") {
		t.Fatal("observation time described as chat start", lastSeen.Events)
	}
	person.inboxes = msg.InboxPage{Outcome: msg.OutcomeForbidden}
	r = panelRequest(t, namedPanel.handler("test-key"), "/activity/messages", nil)
	denied := decodePanel[activityMessagesPage](t, r.Code, r.Body.Bytes())
	if denied.Events[0].ToChat != "Unnamed chat" {
		t.Fatal("refused operator inventory leaked title", denied.Events)
	}
	r = panelRequest(t, panel, "/activity/message?id=sender-1", nil)
	body := decodePanel[activityMessageBody](t, r.Code, r.Body.Bytes())
	if body.Body != person.page.Messages[0].Body || body.Headline != "<script>quoted</script>" || len(person.ids) != 1 || r.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(body, person.ids, r.Header())
	}
	person.page.Messages[0].Header.ExpiresUnixMs = time.Now().Add(-time.Second).UnixMilli()
	r = panelRequest(t, panel, "/activity/message?id=sender-1", nil)
	if expired := decodePanel[activityMessageBody](t, r.Code, r.Body.Bytes()); expired.ID != "" || expired.Body != "" || expired.Headline != "" {
		t.Fatal("expired body", expired)
	}
	person.page.Outcome = msg.OutcomeForbidden
	r = panelRequest(t, panel, "/activity/message?id=sender-1", nil)
	if forbidden := decodePanel[activityMessageBody](t, r.Code, r.Body.Bytes()); forbidden.Outcome != "forbidden" || forbidden.Body != "" {
		t.Fatal(forbidden)
	}
}
