package main

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"time"

	job "github.com/openabstractions/abstraction-job/go"
	rec "idl/out/go/rec"
)

// wirePeer is a job.Store reached through the generated envelope.
//
// Nothing here spells a field name, an operation name or a refusal word: the
// definition does, and this file only says which operation carries which
// argument. The framing — one exchange per connection, a line delimiter — is
// the transport's and is written out here because the generator refuses to
// emit it.
type wirePeer struct{ address string }

func (p wirePeer) do(req *rec.Request) (*rec.Response, error) {
	conn, err := net.Dial("tcp", p.address)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if _, err := conn.Write(frame(rec.EncodeRequest(req))); err != nil {
		return nil, err
	}
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		return nil, err
	}
	resp, err := rec.DecodeResponse(line)
	if err != nil {
		return nil, err
	}
	if resp.Kind != "" {
		return resp, refusalOf(resp.Kind, resp.Error)
	}
	return resp, nil
}

func frame(b []byte) []byte { return append(b, '\n') }

func (p wirePeer) record(resp *rec.Response, err error) (*job.Record, error) {
	if err != nil {
		return nil, err
	}
	return job.Decode([]byte(resp.Record))
}

func (p wirePeer) Submit(r job.Record) (string, error) {
	body, err := r.EncodeProposal()
	if err != nil {
		return "", err
	}
	resp, err := p.do(&rec.Request{Op: "submit", Record: string(body)})
	if err != nil {
		return "", err
	}
	return resp.Id, nil
}

func (p wirePeer) Load(id string) (*job.Record, error) {
	return p.record(p.do(&rec.Request{Op: "load", Id: id}))
}

func (p wirePeer) List() ([]*job.Record, error) { return p.many(&rec.Request{Op: "list"}) }

func (p wirePeer) Orphans() ([]*job.Record, error) { return p.many(&rec.Request{Op: "orphans"}) }

func (p wirePeer) many(req *rec.Request) ([]*job.Record, error) {
	resp, err := p.do(req)
	if err != nil {
		return nil, err
	}
	out := make([]*job.Record, 0, len(resp.Records))
	for _, raw := range resp.Records {
		r, err := job.Decode([]byte(raw))
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

func (p wirePeer) Claimable(r *job.Record) bool {
	return !r.State.Terminal() && !r.Lease.Held(time.Now())
}

func (p wirePeer) Claim(id, owner string, ttl time.Duration) (*job.Record, error) {
	return p.record(p.do(&rec.Request{Op: "claim", Id: id, Owner: owner, TtlMs: ttl.Milliseconds()}))
}

func (p wirePeer) Renew(id string, epoch int64, ttl time.Duration) (*job.Record, error) {
	return p.record(p.do(&rec.Request{Op: "renew", Id: id, Epoch: epoch, TtlMs: ttl.Milliseconds()}))
}

func (p wirePeer) Release(id string, epoch int64) error {
	_, err := p.do(&rec.Request{Op: "release", Id: id, Epoch: epoch})
	return err
}

func (p wirePeer) SetIntent(id string, want job.Want, by string) (*job.Record, error) {
	return p.record(p.do(&rec.Request{Op: "set_intent", Id: id, Want: string(want), By: by}))
}

func (p wirePeer) Recall(id string, epoch int64, reason, by string, grace time.Duration) (*job.Record, error) {
	return p.record(p.do(&rec.Request{Op: "recall", Id: id, Epoch: epoch, Reason: reason, By: by, TtlMs: grace.Milliseconds()}))
}

// Update reads, mutates what it read, and writes conditional on the record
// still being that. The closure cannot cross a socket, which is why the read
// and the write are two operations here and one everywhere else.
func (p wirePeer) Update(id string, epoch int64, mutate func(*job.Record) error) (*job.Record, error) {
	current, err := p.Load(id)
	if err != nil {
		return nil, err
	}
	base, err := current.Encode()
	if err != nil {
		return nil, err
	}
	if err := mutate(current); err != nil {
		return nil, err
	}
	next, err := current.Encode()
	if err != nil {
		return nil, err
	}
	return p.record(p.do(&rec.Request{Op: "write", Id: id, Epoch: epoch, Base: string(base), Record: string(next)}))
}

var _ job.Store = wirePeer{}

// refusalOf turns the verdict back into the sentinel a caller compares with
// errors.Is. A refusal that arrives as prose has lost the only part of itself
// the caller can branch on.
func refusalOf(verdict, text string) error {
	var base error
	switch verdict {
	case "not_found":
		base = job.ErrNotFound
	case "lease_held":
		base = job.ErrLeaseHeld
	case "stale_epoch":
		base = job.ErrStaleEpoch
	case "conflict":
		base = job.ErrConflict
	case "lease_expired":
		base = job.ErrLeaseExpiry
	case "terminal":
		base = job.ErrTerminal
	case "invalid":
		base = job.ErrInvalid
	case "unknown_schema":
		base = job.ErrUnknownSchema
	default:
		return errors.New(text)
	}
	return fmt.Errorf("%w: %s", base, text)
}

func verdictOf(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, job.ErrNotFound):
		return "not_found"
	case errors.Is(err, job.ErrLeaseHeld):
		return "lease_held"
	case errors.Is(err, job.ErrStaleEpoch):
		return "stale_epoch"
	case errors.Is(err, job.ErrConflict):
		return "conflict"
	case errors.Is(err, job.ErrLeaseExpiry):
		return "lease_expired"
	case errors.Is(err, job.ErrTerminal):
		return "terminal"
	case errors.Is(err, job.ErrUnknownSchema):
		return "unknown_schema"
	case errors.Is(err, job.ErrInvalid):
		return "invalid"
	}
	return "other"
}
