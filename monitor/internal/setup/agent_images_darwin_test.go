package setup

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstraction-identity/go/privatedir"
)

func TestDarwinAgentImagesAreDistinctExecutableAndRevalidateNames(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "gateway")
	if err := os.WriteFile(source, []byte("owned copy fixture"), 0700); err != nil {
		t.Fatal(err)
	}
	var images []*AgentImage
	for _, party := range []AgentParty{AgentClaude, AgentCodex} {
		image, err := copyAgentImage(context.Background(), source, root, party)
		if err != nil {
			t.Fatal(err)
		}
		defer image.Close()
		images = append(images, image)
		if filepath.Base(image.Program()) != "oa-"+string(party)+"-integration" {
			t.Fatal(image.Program())
		}
		info, err := os.Stat(image.Program())
		if err != nil || info.Mode().Perm() != 0700 {
			t.Fatal("private executable mode", info, err)
		}
		if err := image.Check(context.Background()); err != nil {
			t.Fatal(err)
		}
		if _, err := copyAgentImage(context.Background(), source, root, party); err == nil {
			t.Fatal("existing image silently adopted")
		}
	}
	if images[0].FileKey() == images[1].FileKey() {
		t.Fatal("shared image identity")
	}
	// POSIX holds permit rename; the named identity check must refuse its replacement.
	image := images[0]
	if err := os.Rename(image.Program(), image.Program()+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(image.Program(), []byte("replacement"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := image.Check(context.Background()); err == nil {
		t.Fatal("replacement accepted")
	}
	if err := image.Close(); err != nil {
		t.Fatal(err)
	}
	if err := image.Check(context.Background()); err == nil {
		t.Fatal("closed image accepted")
	}
	if agentGatewayPath("/private/fixture/bin/openabstractions") != "/private/fixture/bin/openabstractions-mcp" {
		t.Fatal("installer sibling layout")
	}
}

func TestDarwinEnrolledAgentImageRestartRequiresCurrentEnrollment(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := privatedir.Tighten(root); err != nil {
		t.Fatal(err)
	}
	program := filepath.Join(root, agentImageName(AgentClaude))
	if err := os.WriteFile(program, []byte("inert enrolled image"), 0700); err != nil {
		t.Fatal(err)
	}
	view := applications.AgentEnrollmentResult{Outcome: applications.OutcomePage, Enrollment: &applications.AgentEnrollment{Application: "oa-claude-integration", Program: program, Revision: "kept", State: applications.AgentEnrollmentStateEnrolled}}
	read := func(context.Context, string) (applications.AgentEnrollmentResult, error) { return view, nil }
	image, err := holdEnrolledAgentImage(context.Background(), root, AgentClaude, read)
	if err != nil {
		t.Fatal(err)
	}
	defer image.Close()
	if err := image.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	view.Enrollment.Revision = "new-revision"
	if err := image.Check(context.Background()); err == nil {
		t.Fatal("changed enrollment accepted")
	}
	view.Enrollment.Revision = "kept"
	view.Enrollment.State = applications.AgentEnrollmentStatePending
	if _, err := holdEnrolledAgentImage(context.Background(), root, AgentClaude, read); err == nil {
		t.Fatal("pending enrollment accepted")
	}
	view.Outcome = applications.OutcomeForbidden
	if _, err := holdEnrolledAgentImage(context.Background(), root, AgentClaude, read); err == nil {
		t.Fatal("unavailable enrollment accepted")
	}
	if err := os.Rename(program, program+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(program, []byte("replacement"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := image.Check(context.Background()); err == nil {
		t.Fatal("replaced restart image accepted")
	}
}

func TestDarwinAgentImagesRefuseLinksForeignTargetsAndCancellation(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "gateway")
	if err := os.WriteFile(source, []byte("fixture"), 0700); err != nil {
		t.Fatal(err)
	}
	link := source + ".link"
	if err := os.Symlink(source, link); err != nil {
		t.Fatal(err)
	}
	if _, err := copyAgentImage(context.Background(), link, root, AgentClaude); err == nil {
		t.Fatal("linked source accepted")
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(source, link); err != nil {
		t.Fatal(err)
	}
	if _, err := copyAgentImage(context.Background(), source, root, AgentClaude); err == nil {
		t.Fatal("multiply named source accepted")
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, agentImageName(AgentClaude))
	if err := os.WriteFile(target, []byte("foreign"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := copyAgentImage(context.Background(), source, root, AgentClaude); err == nil {
		t.Fatal("foreign target accepted")
	}
	if body, err := os.ReadFile(target); err != nil || string(body) != "foreign" {
		t.Fatal("foreign target changed", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := copyAgentImage(ctx, source, root, AgentCodex); err == nil {
		t.Fatal("cancelled copy accepted")
	}
	if _, err := os.Stat(filepath.Join(root, agentImageName(AgentCodex))); !os.IsNotExist(err) {
		t.Fatal("cancelled copy created target", err)
	}
}
