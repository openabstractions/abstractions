package main

import (
	"errors"
	"log"

	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/appmodel"
	"github.com/openabstractions/abstraction-identity/go/replyfit"
)

// Reuse the actual generated wrapper, including imported record indentation.
// The embedded interface is never dispatched: this closed receiver overrides
// every operation admitted by measureApplicationV3Reply.
type applicationV3BoundReceiver struct {
	applications.Applications
	page     applications.Page
	describe applications.DescribeResult
	commands applications.CommandsResult
	call     applications.CallResult
}

func (r *applicationV3BoundReceiver) Observe(applications.Query) (applications.Page, error) {
	return r.page, nil
}
func (r *applicationV3BoundReceiver) Describe(appmodel.ApplicationsQuery) (applications.DescribeResult, error) {
	return r.describe, nil
}
func (r *applicationV3BoundReceiver) Commands(appmodel.CommandsQuery) (applications.CommandsResult, error) {
	return r.commands, nil
}
func (r *applicationV3BoundReceiver) Call(string, appmodel.GatewayCall) (applications.CallResult, error) {
	return r.call, nil
}

type applicationV3BoundTransport struct {
	receiver *applications.ApplicationsDispatcher
	size     int
}

func (t *applicationV3BoundTransport) ExchangeFrame(frame []byte) ([]byte, error) {
	answer, err := t.receiver.ExchangeFrame(frame)
	t.size = len(answer)
	return answer, err
}
func measureApplicationV3Reply(v any) (int, error) {
	r := &applicationV3BoundReceiver{}
	t := &applicationV3BoundTransport{receiver: &applications.ApplicationsDispatcher{Handler: r}}
	c := applications.NewApplicationsClient(t)
	var err error
	switch value := v.(type) {
	case applications.Page:
		r.page = value
		_, err = c.Observe(applications.Query{Limit: 1})
	case applications.DescribeResult:
		r.describe = value
		_, err = c.Describe(appmodel.ApplicationsQuery{})
	case applications.CommandsResult:
		r.commands = value
		_, err = c.Commands(appmodel.CommandsQuery{Application: "measure", Instance: "measure"})
	case applications.CallResult:
		r.call = value
		_, err = c.Call("measure", appmodel.GatewayCall{Application: "measure", Instance: "measure", Tool: "app_describe", Arguments: appmodel.Raw("{}")})
	default:
		return 0, errors.New("applications: unsupported reply measurement")
	}
	return t.size, err
}

func applicationV3ReplyFits(v any) bool {
	size, err := measureApplicationV3Reply(v)
	return err == nil && size <= ownedApplicationFrameLimit
}

// errApplicationV3ReplyBytes is a complete reply over the owned frame limit;
// replyfit reports it as the why "reply_bytes".
var errApplicationV3ReplyBytes = errors.New("reply_bytes")

// fitApplicationV3Reply keeps a Describe or Commands page readable when one
// listed entry fails the generated writer or the frame limit: that entry is
// dropped whole, an overflowing list is cut, and each change is logged with
// its field path (replyfit.FitWhole). A fault outside every list returns
// the reason field:<path>:<why>.
func fitApplicationV3Reply[T any](method string, value T) (T, error) {
	return replyfit.FitWhole(value, func(v T) error {
		size, err := measureApplicationV3Reply(v)
		if err != nil {
			return err
		}
		if size > ownedApplicationFrameLimit {
			return errApplicationV3ReplyBytes
		}
		return nil
	}, func(line string) { log.Printf("applications %s reply: %s", method, line) })
}

func boundedApplicationV3CallReply(result applications.CallResult) applications.CallResult {
	if !applicationV3ReplyFits(result) {
		return applications.CallResult{Outcome: applications.OutcomeUnknown, Reason: "bound:reply_bytes"}
	}
	return result
}
