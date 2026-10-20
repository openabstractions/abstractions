package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstractions/mcp-gateway/startprofile"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

type claudeStartupFixture struct {
	c          *claudePanelConnections
	descriptor wire.ApplicationDescriptor
	enrollment applications.AgentEnrollmentResult
	writes     int
	unknown    bool
	refuse     bool
	beforeSet  func()
	cli        string
	interrupt  string
	transition *applications.ActivationConfiguration
}

func newClaudeStartupFixture(t *testing.T) *claudeStartupFixture {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	cli := filepath.Join(root, "inert-code.exe")
	if err := os.WriteFile(cli, []byte("never executed"), 0600); err != nil {
		t.Fatal(err)
	}
	id, err := fileid.Resolve(ctx, fileid.Self, cli, fileid.Options{Links: fileid.RefuseLinks})
	if err != nil {
		t.Fatal(err)
	}
	f := &claudeStartupFixture{cli: cli, descriptor: wire.ApplicationDescriptor{Name: claudeIntegrationApplication, Program: filepath.Join(root, "kept-helper.exe"), Title: "Kept title", StartGuidance: "Preserve guidance"}}
	f.enrollment = applications.AgentEnrollmentResult{Outcome: applications.OutcomePage, Enrollment: &applications.AgentEnrollment{Application: f.descriptor.Name, Program: f.descriptor.Program, Revision: "original-person-keep", State: applications.AgentEnrollmentStateEnrolled}}
	// Exercise generated @2 full-descriptor receiving and @3 conditional writes.
	transport := nativeApplicationExchange(func(raw []byte) ([]byte, error) {
		var frame struct {
			Service, Method string
			Arguments       json.RawMessage
		}
		if err := json.Unmarshal(raw, &frame); err != nil {
			t.Fatal(err)
		}
		var value []byte
		switch frame.Service + "/" + frame.Method {
		case "abstraction.facade/applications@2/Observe":
			value = wire.EncodeApplicationPage(&wire.ApplicationPage{Outcome: wire.ApplicationOutcomePage, Applications: []wire.ApplicationEntry{{Scope: wire.ScopeLocal, Descriptor: f.descriptor}}})
		case "abstraction.facade/applications@3/ReadAgentEnrollment":
			value = applications.EncodeAgentEnrollmentResult(&f.enrollment)
		case "abstraction.facade/applications@3/ReconcileActivation":
			var args struct{ Expected json.RawMessage }
			if err := json.Unmarshal(frame.Arguments, &args); err != nil {
				t.Fatal(err)
			}
			expected, err := wire.DecodeApplicationDescriptor(args.Expected)
			if err != nil {
				t.Fatal(err)
			}
			outcome := wire.ApplicationOutcomeConflict
			if f.transition != nil && reflect.DeepEqual(*expected, f.descriptor) {
				f.descriptor.Activation = f.transition.Activation
				f.transition = nil
				f.enrollment.Enrollment.State = applications.AgentEnrollmentStateEnrolled
				f.writes++
				outcome = wire.ApplicationOutcomeApplied
			}
			value = wire.EncodeApplicationChange(&wire.ApplicationChange{Outcome: outcome})
		case "abstraction.facade/applications@3/SetActivation":
			var args struct{ Request json.RawMessage }
			if err := json.Unmarshal(frame.Arguments, &args); err != nil {
				t.Fatal(err)
			}
			request, err := applications.DecodeActivationConfiguration(args.Request)
			if err != nil {
				t.Fatal(err)
			}
			if f.beforeSet != nil {
				f.beforeSet()
			}
			outcome := wire.ApplicationOutcomeApplied
			desired := request.Expected
			desired.Activation = request.Activation
			if f.transition != nil {
				if !reflect.DeepEqual(request, f.transition) {
					outcome = wire.ApplicationOutcomeConflict
				} else {
					f.descriptor.Activation = request.Activation
					f.enrollment.Enrollment.State = applications.AgentEnrollmentStateEnrolled
					f.transition = nil
					f.writes++
				}
			} else if f.interrupt != "" {
				f.transition = request
				f.enrollment.Enrollment.State = applications.AgentEnrollmentStateNone
				if f.interrupt == "descriptor" {
					f.descriptor.Activation = request.Activation
				}
				f.interrupt = ""
				outcome = wire.ApplicationOutcomeUnavailable
			} else if f.refuse || !reflect.DeepEqual(request.Expected, f.descriptor) && !reflect.DeepEqual(desired, f.descriptor) {
				outcome = wire.ApplicationOutcomeConflict
			} else if !reflect.DeepEqual(desired, f.descriptor) {
				f.writes++
				f.descriptor.Activation = request.Activation
			}
			if f.unknown {
				return nil, errors.New("accepted response lost")
			}
			value = wire.EncodeApplicationChange(&wire.ApplicationChange{Outcome: outcome})
		default:
			t.Fatalf("unexpected effect: %s", raw)
		}
		return json.Marshal(map[string]any{"version": 1, "service": frame.Service, "method": frame.Method, "ok": true, "payload": map[string]any{"value": json.RawMessage(value)}})
	})
	legacy, native := wire.NewApplicationsClient(transport), applications.NewApplicationsClient(transport)
	working := filepath.Join(root, "work")
	if err := os.Mkdir(working, 0700); err != nil {
		t.Fatal(err)
	}
	c := &claudePanelConnections{revision: 1, state: func() (string, error) { return root, nil }, picker: func(context.Context) (string, error) { return working, nil }}
	c.startupRead = func(ctx context.Context) (wire.ApplicationDescriptor, applications.AgentEnrollmentResult, error) {
		page, err := legacy.Observe("", 0)
		if err != nil {
			return wire.ApplicationDescriptor{}, applications.AgentEnrollmentResult{}, err
		}
		d, err := claudeStartupDescriptor(page)
		if err != nil {
			return d, applications.AgentEnrollmentResult{}, err
		}
		r, err := native.ReadAgentEnrollment(d.Name)
		return d, r, err
	}
	c.startupSet = func(ctx context.Context, r applications.ActivationConfiguration) (wire.ApplicationChange, error) {
		return native.SetActivation(r)
	}
	c.startupFinish = func(ctx context.Context, expected wire.ApplicationDescriptor) (wire.ApplicationChange, error) {
		return native.ReconcileActivation(expected)
	}
	c.startupImage = func(context.Context) (claudePanelImage, error) {
		return &claudePanelFixtureImage{program: f.descriptor.Program}, nil
	}
	c.startupCLI = func(context.Context) (claudePanelImage, error) {
		return &claudePanelFixtureImage{program: cli, key: id.Key()}, nil
	}
	f.c = c
	return f
}

func startupEdit() claudeStartupEdit {
	return claudeStartupEdit{Revision: "1", Action: "configure", Model: "explicit-model", TimeoutMS: 1000, BudgetCents: 1, OutputBytes: 4096, MemoryBytes: 256 << 20}
}

func TestClaudeStartupConditionalNativeConfigureEditDisable(t *testing.T) {
	f := newClaudeStartupFixture(t)
	ctx := context.Background()
	before := f.descriptor
	keep := *f.enrollment.Enrollment
	view, err := f.c.readStartup(ctx, "1")
	if err != nil || view.Configured || view.Model != "" || view.TimeoutMS != 0 {
		t.Fatal(view, err)
	}
	if _, err := f.c.chooseStartupFolder(ctx, "1"); err != nil {
		t.Fatal(err)
	}
	if err := f.c.changeStartup(ctx, startupEdit()); err != nil {
		t.Fatal(err)
	}
	p, err := startprofile.Parse(f.descriptor.Activation.Arguments)
	if err != nil || p.Context == "" || p.Model != "explicit-model" {
		t.Fatal(p, err)
	}
	firstContext := p.Context
	if _, err := f.c.readStartup(ctx, "1"); err != nil {
		t.Fatal(err)
	}
	edit := startupEdit()
	edit.Model = "selected-other-model"
	if err := f.c.changeStartup(ctx, edit); err != nil {
		t.Fatal(err)
	}
	p, err = startprofile.Parse(f.descriptor.Activation.Arguments)
	if err != nil || p.Context != firstContext {
		t.Fatal(p, err)
	}
	if _, err := f.c.readStartup(ctx, "1"); err != nil {
		t.Fatal(err)
	}
	if err := f.c.changeStartup(ctx, claudeStartupEdit{Revision: "1", Action: "disable"}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.descriptor, before) || !reflect.DeepEqual(*f.enrollment.Enrollment, keep) || f.writes != 3 {
		t.Fatal("configuration changed connection/Keep", f.descriptor, f.enrollment, f.writes)
	}
	if _, err := f.c.readStartup(ctx, "1"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.c.chooseStartupFolder(ctx, "1"); err != nil {
		t.Fatal(err)
	}
	if err := f.c.changeStartup(ctx, startupEdit()); err != nil {
		t.Fatal(err)
	}
	p, _ = startprofile.Parse(f.descriptor.Activation.Arguments)
	if p.Context == firstContext {
		t.Fatal("removed context reused")
	}
}

func TestClaudeStartupRefusalsAndUncertainWriteRequireReadback(t *testing.T) {
	for _, mode := range []string{"revision", "enrollment", "pending", "descriptor", "folder", "bounds", "CAS", "closed", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			f := newClaudeStartupFixture(t)
			ctx := context.Background()
			if _, err := f.c.readStartup(ctx, "1"); err != nil {
				t.Fatal(err)
			}
			if _, err := f.c.chooseStartupFolder(ctx, "1"); err != nil {
				t.Fatal(err)
			}
			edit := startupEdit()
			switch mode {
			case "revision":
				edit.Revision = "old"
			case "enrollment":
				f.enrollment.Enrollment.Revision = "changed"
			case "pending":
				f.enrollment.Enrollment.State = applications.AgentEnrollmentStatePending
			case "descriptor":
				f.descriptor.StartGuidance = "changed"
			case "folder":
				if err := os.Rename(f.c.startupCapture.folder.Path(), f.c.startupCapture.folder.Path()+"-moved"); err != nil {
					t.Fatal(err)
				}
			case "bounds":
				edit.TimeoutMS = 0
			case "CAS":
				f.refuse = true
			case "closed":
				f.c.closed = true
			case "unknown":
				f.unknown = true
			}
			if err := f.c.changeStartup(ctx, edit); err == nil {
				t.Fatal("accepted", mode)
			}
			if mode == "unknown" {
				if f.writes != 1 {
					t.Fatal(f.writes)
				}
				f.unknown = false
				if err := f.c.changeStartup(ctx, edit); err == nil || f.writes != 1 {
					t.Fatal("unknown retry escaped readback", err, f.writes)
				}
				if view, err := f.c.readStartup(ctx, "1"); err != nil || !view.Configured || view.Recovery {
					t.Fatal(view, err)
				}
				if err := f.c.changeStartup(ctx, claudeStartupEdit{Revision: "1", Action: "finish"}); err == nil || f.writes != 1 {
					t.Fatal(err, f.writes)
				}
			} else if f.writes != 0 {
				t.Fatal("refusal wrote configuration", f.writes)
			}
		})
	}
}

func TestClaudeStartupInterruptedNativeTransactionReconcilesExactRequest(t *testing.T) {
	for _, stage := range []string{"prepared", "descriptor"} {
		t.Run(stage, func(t *testing.T) {
			f := newClaudeStartupFixture(t)
			ctx := context.Background()
			keep := *f.enrollment.Enrollment
			if _, err := f.c.readStartup(ctx, "1"); err != nil {
				t.Fatal(err)
			}
			if _, err := f.c.chooseStartupFolder(ctx, "1"); err != nil {
				t.Fatal(err)
			}
			f.interrupt = stage
			if err := f.c.changeStartup(ctx, startupEdit()); err == nil {
				t.Fatal("partial transaction reported saved")
			}
			request := f.transition
			if request == nil {
				t.Fatal("native transaction absent")
			}
			profile, _ := startprofile.Parse(request.Activation.Arguments)
			f.c = &claudePanelConnections{revision: 1, startupRead: f.c.startupRead, startupSet: f.c.startupSet, startupFinish: f.c.startupFinish}
			if view, err := f.c.readStartup(ctx, "1"); err != nil || !view.Recovery {
				t.Fatal(view, err)
			}
			if err := f.c.changeStartup(ctx, claudeStartupEdit{Revision: "1", Action: "finish"}); err != nil {
				t.Fatal(err)
			}
			current, _ := startprofile.Parse(f.descriptor.Activation.Arguments)
			if current.Context != profile.Context || f.transition != nil || !reflect.DeepEqual(*f.enrollment.Enrollment, keep) || f.writes != 1 {
				t.Fatal("recovery changed context/Keep", current, f.writes)
			}
		})
	}
}

func TestClaudeStartupFailedNativeConstructorRemainsNilInterface(t *testing.T) {
	for _, failure := range []error{errors.New("pending native identity"), nil} {
		image, err := selectClaudeStartupImage(context.Background(), func(context.Context) (*setup.EnrolledAgentImage, error) { return nil, failure })
		if image != nil || err != failure {
			t.Fatal("typed nil escaped", image, err)
		}
		f := newClaudeStartupFixture(t)
		if _, err := f.c.readStartup(context.Background(), "1"); err != nil {
			t.Fatal(err)
		}
		f.c.startupImage = func(context.Context) (claudePanelImage, error) { return image, err }
		if err := f.c.changeStartup(context.Background(), claudeStartupEdit{Revision: "1", Action: "disable"}); err == nil || f.writes != 0 {
			t.Fatal(err, f.writes)
		}
	}
}

func TestClaudeStartupActualRenderedPage(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node required")
	}
	// The owner declined this proposed presentation. Exercise its retained
	// preview separately; it is not part of the ordinary connection page.
	input, _ := json.Marshal(map[string]string{"markup": claudeStartupPage, "script": claudeStartupScript})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, node, "-e", claudeStartupFixtureJS)
	command.Stdin = bytes.NewReader(input)
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("rendered startup controls: %v %s", err, out)
	}
}

const claudeStartupFixtureJS = `
const fs=require('node:fs'),vm=require('node:vm'),assert=require('node:assert/strict'),input=JSON.parse(fs.readFileSync(0,'utf8')),nodes=new Map(),pending=[],events={};
for(const m of input.markup.matchAll(/\bid="([^"]+)"/g)){assert(!nodes.has(m[1]),m[1]);nodes.set(m[1],{value:'',textContent:'',disabled:false,hidden:false})}
const el=id=>nodes.get(id)||null,ctx=vm.createContext({AbortController,el,claudeClosed:false,claudeViewRevision:'1',fail:(id,e)=>el(id).textContent=String(e),addEventListener:(name,fn)=>events[name]=fn,call:(path,body,signal)=>new Promise((resolve,reject)=>pending.push({path,body,signal,resolve,reject}))});
const tick=()=>new Promise(setImmediate),page={outcome:'page',revision:'1',configured:false,model:'',folder:'',timeout_ms:0,budget_cents:0,output_bytes:1048576,memory_bytes:25769803776};
(async()=>{vm.runInContext(input.script,ctx);vm.runInContext('drawClaudeStartupAvailability({connections:[{kind:"user",registration:"saved",helper_state:"unknown"}]})',ctx);
assert.equal(el('claude-startup-edit').disabled,false);const open=el('claude-startup-edit').onclick();pending[0].resolve(page);await open;
for(const id of ['model','seconds','cost'])assert.equal(el('claude-startup-'+id).value,'');assert.equal(el('claude-startup-disable').disabled,true);assert.equal(el('claude-startup-output').value,1024);assert.equal(el('claude-startup-memory').value,24576);
await el('claude-startup-form').onsubmit({preventDefault(){}});assert.equal(pending.length,1,'empty values must not save');
const choose=el('claude-startup-folder').onclick();pending[1].resolve({folder:'Chosen folder'});await choose;
for(const [id,value]of [['model','explicit-model'],['seconds','2'],['cost','3'],['output','64'],['memory','256']])el('claude-startup-'+id).value=value;
const save=el('claude-startup-form').onsubmit({preventDefault(){}});assert.equal(pending[2].path,'/connections/claude/startup/configure');assert.equal(pending[2].body.timeout_ms,2000);assert(!('context'in pending[2].body));assert(!('cli_file_id'in pending[2].body));pending[2].resolve({outcome:'saved'});await save;assert.match(el('claude-startup-status').textContent,/settings saved/);
const reopen=el('claude-startup-edit').onclick();pending[3].resolve({...page,configured:true,model:'saved-model',timeout_ms:2000,budget_cents:3,output_bytes:4096,memory_bytes:256*1024*1024});await reopen;assert.equal(el('claude-startup-disable').disabled,false);
const disable=el('claude-startup-disable').onclick();assert.deepEqual(Object.keys(pending[4].body),['revision']);pending[4].reject(new Error('response lost'));await disable;assert.match(el('claude-startup-save-status').textContent,/finish any interrupted/);
const recover=el('claude-startup-edit').onclick();pending[5].resolve({...page,recovery:true});await recover;assert.equal(el('claude-startup-save').disabled,true);assert.equal(el('claude-startup-reconcile').disabled,false);const reconcile=el('claude-startup-reconcile').onclick();assert.equal(pending[6].path,'/connections/claude/startup/finish');assert.deepEqual(Object.keys(pending[6].body),['revision']);pending[6].resolve({outcome:'saved'});await reconcile;const late=el('claude-startup-edit').onclick();events.pagehide();pending[7].resolve(page);await late;assert.equal(el('claude-startup-form').hidden,true);
})().catch(e=>{console.error(e);process.exitCode=1});`
