package main

import (
	"bufio"
	"bytes"
	"fmt"
	"net"
	"time"

	job "github.com/openabstractions/abstraction-job/go"
	rec "idl/out/go/rec"
)

// serveGenerated is the far half built on the generated envelope. What it
// dispatches to is the binding's business and is written here; what travels is
// the definition's and is generated.
func serveGenerated(ln net.Listener, store job.Store) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go func() {
			defer conn.Close()
			line, err := bufio.NewReader(conn).ReadBytes('\n')
			if err != nil {
				return
			}
			req, err := rec.DecodeRequest(line)
			if err != nil {
				reply(conn, &rec.Response{Kind: "invalid", Error: err.Error()})
				return
			}
			reply(conn, dispatch(store, req))
		}()
	}
}

func reply(conn net.Conn, resp *rec.Response) {
	conn.Write(frame(rec.EncodeResponse(resp)))
}

func refuse(err error) *rec.Response {
	return &rec.Response{Kind: verdictOf(err), Error: err.Error()}
}

func one(r *job.Record, err error) *rec.Response {
	if err != nil {
		return refuse(err)
	}
	b, err := r.Encode()
	if err != nil {
		return refuse(err)
	}
	return &rec.Response{Record: string(b)}
}

func many(rs []*job.Record, err error) *rec.Response {
	if err != nil {
		return refuse(err)
	}
	out := make([]rec.Raw, 0, len(rs))
	for _, r := range rs {
		b, err := r.Encode()
		if err != nil {
			return refuse(err)
		}
		out = append(out, rec.Raw(b))
	}
	return &rec.Response{Records: out}
}

func dispatch(store job.Store, req *rec.Request) *rec.Response {
	if !rec.IsOperation(req.Op) {
		return &rec.Response{Kind: rec.UnknownOperation, Error: "unknown op " + req.Op}
	}
	ttl := time.Duration(req.TtlMs) * time.Millisecond
	switch req.Op {
	case "submit":
		r, err := job.DecodeProposal([]byte(req.Record))
		if err != nil {
			return refuse(err)
		}
		id, err := store.Submit(*r)
		if err != nil {
			return refuse(err)
		}
		return &rec.Response{Id: id}
	case "load":
		return one(store.Load(req.Id))
	case "list":
		return many(store.List())
	case "orphans":
		return many(store.Orphans())
	case "claimable":
		r, err := store.Load(req.Id)
		if err != nil {
			return refuse(err)
		}
		return &rec.Response{Bool: store.Claimable(r)}
	case "claim":
		return one(store.Claim(req.Id, req.Owner, ttl))
	case "renew":
		return one(store.Renew(req.Id, req.Epoch, ttl))
	case "release":
		if err := store.Release(req.Id, req.Epoch); err != nil {
			return refuse(err)
		}
		return &rec.Response{}
	case "set_intent":
		return one(store.SetIntent(req.Id, job.Want(req.Want), req.By))
	case "recall":
		return one(store.Recall(req.Id, req.Epoch, req.Reason, req.By, ttl))
	case "write":
		return one(applyWrite(store, req))
	}
	return &rec.Response{Kind: rec.UnknownOperation, Error: "unknown op " + req.Op}
}

func applyWrite(store job.Store, req *rec.Request) (*job.Record, error) {
	want, err := job.Decode([]byte(req.Record))
	if err != nil {
		return nil, err
	}
	if req.Base == "" {
		return nil, fmt.Errorf("%w: a write must present the record it was computed from", job.ErrInvalid)
	}
	return store.Update(req.Id, req.Epoch, func(held *job.Record) error {
		same, err := unchanged(held, req.Base)
		if err != nil {
			return err
		}
		if !same {
			return fmt.Errorf("%w: %s", job.ErrConflict, req.Id)
		}
		held.State = want.State
		held.Progress = want.Progress
		held.Checkpoint = want.Checkpoint
		held.Delegation = want.Delegation
		held.Error = want.Error
		held.Extensions = want.Extensions
		return nil
	})
}

// unchanged compares canonical forms rather than the bytes as they arrived,
// because the transport is free to reflow the document and does.
func unchanged(held *job.Record, base rec.Raw) (bool, error) {
	asRead, err := job.Decode([]byte(base))
	if err != nil {
		return false, err
	}
	a, err := held.Encode()
	if err != nil {
		return false, err
	}
	b, err := asRead.Encode()
	if err != nil {
		return false, err
	}
	return bytes.Equal(a, b), nil
}
