package main

import (
	"context"
	"errors"
	"sync"
	"time"
)

// applicationAgentImageRecheck bounds how long admission and the current
// enrollment read reuse one verified agent image while its file stamp stays
// unchanged. Admission runs on every native inbox check and on each message
// request. The enrollment read runs for the sender and receiver of the
// messages a headers or body read, a listing or a progress pass reconciles;
// on the owner's PC in 0.8.0 it verified each connected helper ten times a
// second and kept 1.5 cores busy. A full verification opens the image twice,
// verifies its whole Authenticode signature and resolves the installed
// gateway through the MSI owner. The stamp, read on every check, notices a
// replaced, relinked, hard-linked or ordinarily rewritten image at the next
// check. On Windows, a deliberate same-user in-place rewrite that restores
// FILE_BASIC_INFO, including ChangeTime, can retain the verdict for at most 60
// seconds from the last full verification; cache hits never extend that
// interval. The interval also covers the facts the stamp cannot see: a change
// in certificate trust or revocation, and a reinstall that moves the shared
// gateway onto this same file. Both are administrative changes a person makes
// on a scale of minutes, so one minute keeps them current while removing all
// but one verification in 600 checks.
const applicationAgentImageRecheck = time.Minute

// applicationAgentStamp is the cheap per-check observation of an agent image:
// its file key in the identity owner's spelling, its name count, size, last
// write and metadata change time, read from one handle that does not follow a
// final link.
type applicationAgentStamp struct {
	Key              string
	Links            uint64
	Size             int64
	Written, Changed int64
}

type applicationAgentVerified struct {
	stamp    applicationAgentStamp
	image    applicationAgentImage
	excluded bool
	at       time.Time
}

// applicationAgentImages holds the last verification per registered program
// and binding. Its lock is independent of the directory lock; verification
// never runs under either.
type applicationAgentImages struct {
	mu      sync.Mutex
	entries map[string]applicationAgentVerified
}

// forget drops every retained verification, so the next admission reads each
// image again.
func (c *applicationAgentImages) forget() {
	c.mu.Lock()
	c.entries = nil
	c.mu.Unlock()
}

// verifyAgentImage reads program's current image and publisher through the
// native identity owner and refuses the installed shared gateway.
func (d *applicationDirectory) verifyAgentImage(ctx context.Context, program, binding string) (applicationAgentImage, error) {
	read := d.agentImage
	if read == nil {
		read = readApplicationAgentImage
	}
	image, err := read(ctx, program, binding)
	if err != nil {
		return image, err
	}
	if d.agentExcluded == nil {
		return image, errors.New("agent enrollment: installed gateway identity unavailable")
	}
	excluded, err := d.agentExcluded(ctx, image.Program, image.Key)
	if err != nil {
		return image, err
	}
	if excluded {
		return image, errApplicationAgentSharedGateway
	}
	return image, nil
}

// admittedAgentImage is verifyAgentImage for admission checks and the current
// enrollment read. It reuses the
// last verification of program and binding while the image's stamp is
// unchanged, names the verified file key and is younger than
// applicationAgentImageRecheck. Any other case verifies again. An image whose
// stamp cannot be read is verified on every call, as before the cache.
func (d *applicationDirectory) admittedAgentImage(ctx context.Context, program, binding string) (applicationAgentImage, error) {
	probe := d.agentStamp
	if probe == nil {
		probe = readApplicationAgentStamp
	}
	stamp, err := probe(program)
	if err != nil {
		return d.verifyAgentImage(ctx, program, binding)
	}
	key := program + "\x00" + binding
	now := d.now()
	d.agentImages.mu.Lock()
	v, ok := d.agentImages.entries[key]
	d.agentImages.mu.Unlock()
	if ok && v.stamp == stamp && v.image.Key == stamp.Key && !now.Before(v.at) && now.Sub(v.at) < applicationAgentImageRecheck {
		if v.excluded {
			return v.image, errApplicationAgentSharedGateway
		}
		return v.image, ctx.Err()
	}
	image, err := d.verifyAgentImage(ctx, program, binding)
	excluded := errors.Is(err, errApplicationAgentSharedGateway)
	if err != nil && !excluded || image.Key != stamp.Key {
		// Failures and a file replaced during verification are not retained.
		return image, err
	}
	d.agentImages.mu.Lock()
	if d.agentImages.entries == nil || len(d.agentImages.entries) >= 64 && !ok {
		d.agentImages.entries = map[string]applicationAgentVerified{}
	}
	d.agentImages.entries[key] = applicationAgentVerified{stamp: stamp, image: image, excluded: excluded, at: now}
	d.agentImages.mu.Unlock()
	return image, err
}
