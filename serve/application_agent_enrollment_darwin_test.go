package main

import (
	"bytes"
	"context"
	"debug/macho"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	asks "github.com/openabstractions/abstraction-asks/go"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
)

func darwinAgentImage(t *testing.T, name string) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	in, err := os.Open(self)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	path := filepath.Join(root, name)
	out, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.Copy(out, in)
	if err = errors.Join(err, out.Close()); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDarwinAgentSocketCannotEstablishProgram(t *testing.T) {
	id, err := applicationToken()
	if err != nil {
		t.Fatal(err)
	}
	listener, err := listen.ListenFramed(mustProviderEndpoint(t, "oa-darwin-agent1771-"+id[:16]), listen.Program)
	if listener != nil {
		listener.Close()
		t.Fatal("socket unexpectedly admitted Program")
	}
	if !errors.Is(err, identity.ErrNotProven) {
		t.Fatal("expected exact native proof refusal", err)
	}
}

func TestDarwinAgentMachORefusesForeignSlicesAndLibraries(t *testing.T) {
	cpu := macho.CpuArm64
	foreign := macho.CpuAmd64
	if runtime.GOARCH == "amd64" {
		cpu, foreign = foreign, cpu
	}
	for _, tc := range []struct {
		name string
		cpu  macho.Cpu
		kind macho.Type
		ok   bool
	}{
		{"native", cpu, macho.TypeExec, true},
		{"foreign", foreign, macho.TypeExec, false},
		{"library", cpu, macho.TypeDylib, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var image bytes.Buffer
			if err := binary.Write(&image, binary.LittleEndian, []uint32{macho.Magic64, uint32(tc.cpu), 0, uint32(tc.kind), 0, 0, 0, 0}); err != nil {
				t.Fatal(err)
			}
			err := applicationAgentMachO(bytes.NewReader(image.Bytes()), int64(image.Len()))
			if (err == nil) != tc.ok {
				t.Fatal("native classification", err)
			}
		})
	}
	if err := applicationAgentMachO(bytes.NewReader([]byte("not Mach-O")), 10); err == nil {
		t.Fatal("malformed image accepted")
	}
}

func TestDarwinAgentImageIsNativeIdentityWithUnknownPublisher(t *testing.T) {
	path := darwinAgentImage(t, "fixture-agent")
	image, err := readApplicationAgentImage(context.Background(), path, "fixture-binding")
	if err != nil {
		t.Fatal(err)
	}
	if image.Key == "" || image.Binding != "fixture-binding" || image.Publisher != (asks.Publisher{}) {
		t.Fatal("invented publisher or missing identity", image)
	}
	for _, name := range []string{"script", "not-executable", "python"} {
		t.Run(name, func(t *testing.T) {
			p := darwinAgentImage(t, name)
			if name == "script" {
				if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			if name == "not-executable" {
				if err := os.Chmod(p, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := readApplicationAgentImage(context.Background(), p, "binding"); err == nil {
				t.Fatal("non-agent image accepted")
			}
		})
	}
	for _, kind := range []string{"hardlink", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			p := darwinAgentImage(t, "agent")
			alias := p + ".alias"
			link := os.Link
			if kind == "symlink" {
				link = os.Symlink
			}
			if err := link(p, alias); err != nil {
				t.Fatal(err)
			}
			if _, err := readApplicationAgentImage(context.Background(), alias, "binding"); err == nil {
				t.Fatal("linked image accepted")
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := readApplicationAgentImage(ctx, path, "binding"); err == nil {
		t.Fatal("cancelled image accepted")
	}
}
