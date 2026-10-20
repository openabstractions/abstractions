package main

import (
	"context"
	"errors"
	"fmt"
	asks "github.com/openabstractions/abstraction-asks/go"
	credservice "github.com/openabstractions/abstraction-credentials/go/service"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-identity/listen"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"
)

type applicationsHost struct {
	listener             listen.Listener
	directory            *applicationDirectory
	v3                   *applicationV3Gateway
	messages             *messageStore
	messageRights        *runtimeRights
	stopMessageRecheck   func()
	messageStartSettings func(context.Context, string) (messageStartSettings, error)
	messagePairing       *runtimePairingState
	messageAudit         *messageAudit
	messageAuditMu       sync.Mutex
	// messageStarts counts claimed receiver starts running as their own
	// tasks, so a caller that must observe their outcome can wait for them.
	messageStarts  messageStartTasks
	messageNotices *runtimeAsks
	messageWatchMu sync.Mutex
	messageWatches map[*nativeMessageWatch]struct{}
	// messageWatchEnds keeps when each program/context's last watch ended,
	// so a waker between two watches still counts as watching.
	messageWatchEnds map[string]time.Time
	owner            string
	report           func(error)
	slowPresence     slowPresenceLines
	ctx              context.Context
	cancel           context.CancelFunc
	once             sync.Once
	workers          sync.WaitGroup
	slots            listen.CallAdmission
	// signIn is the installed runtime's sign-in read (ReadSignInStart).
	signIn signInReader
	// setup holds the one pending setup request (RequestSetup).
	setup setupController

	// messageProgressStarts lets each progress pass resume within its slice.
	messageProgressStarts messageProgressStarts
}

func listenApplications(endpoint string, directory *applicationDirectory, owner string, report func(error)) (*applicationsHost, error) {
	l, err := listen.ListenFramed(endpoint, listen.Program)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	h := &applicationsHost{listener: listen.Sessions(l, listen.SessionOptions{MaxSessions: 64}), directory: directory, owner: owner, report: report, ctx: ctx, cancel: cancel, slots: listen.NewCallAdmission(64)}
	if filepath.IsAbs(directory.dir) {
		h.setup.receiptPath = filepath.Join(directory.dir, "setup-receipt.json")
	}
	return h, nil
}

func (h *applicationsHost) Close() error {
	var err error
	h.once.Do(func() {
		h.cancel()
		if h.stopMessageRecheck != nil {
			h.stopMessageRecheck()
		}
		err = h.listener.Close()
		h.directory.closeNativeInboxes()
		h.messageAudit.close()
	})
	return err
}

func (h *applicationsHost) Serve(ctx context.Context) error {
	if h.messages != nil {
		h.workers.Add(1)
		go func() {
			defer h.workers.Done()
			ticker := time.NewTicker(time.Second)
			defer ticker.Stop()
			h.runMessageProgress(ticker.C)
		}()
	}
	//unchecked: Close is idempotent (sync.Once); this async cancellation callback has no caller to report the error to, and Serve's own deferred Close below is the same no-op afterward
	stop := context.AfterFunc(ctx, func() { h.Close() })
	defer stop()
	defer func() {
		h.workers.Wait()
		if h.v3 != nil {
			if err := h.v3.close(); err != nil && h.report != nil {
				h.report(err)
			}
		}
	}()
	defer h.Close()
	for {
		conn, err := h.listener.Accept()
		if err != nil {
			if ctx.Err() != nil || h.ctx.Err() != nil {
				return nil
			}
			return err
		}
		release, admitted := h.slots.Take(conn)
		if !admitted {
			continue
		}
		h.workers.Add(1)
		go func() {
			defer h.workers.Done()
			defer release()
			defer conn.Close()
			// The request is read within the ordinary budget. A command call
			// or an open then runs as long as its caller waits; every other
			// call keeps the ordinary budget.
			callCtx, cancel := context.WithTimeout(h.ctx, applicationsCommandCeiling)
			defer cancel()
			receiving := time.AfterFunc(applicationsCallBudget, func() { conn.Close() })
			call, err := listen.ReceiveFramed(callCtx, conn, listen.Program, 1<<20)
			if call != nil {
				defer call.Close()
			}
			if !receiving.Stop() && err == nil {
				err = context.DeadlineExceeded
			}
			if err == nil {
				budget := callCtx
				if !applicationsCallerTimed(call.Frame) {
					var end context.CancelFunc
					budget, end = context.WithTimeout(callCtx, applicationsCallBudget)
					defer end()
				}
				var reply []byte
				if reply, err = h.exchangeApplicationFrame(budget, call); err == nil {
					err = call.Reply(reply)
				}
			}
			if err != nil && h.report != nil && h.ctx.Err() == nil && !applicationsCallerLeft(err) {
				h.report(fmt.Errorf("applications: %w", err))
			}
		}()
	}
}

// applicationsCallBudget is the budget of an applications call other than a
// command call, and of reading any request. Tests shorten it.
var applicationsCallBudget = (maxObserveWait + 5000) * time.Millisecond

// applicationsCommandCeiling bounds a command call or an open, whose caller
// sets its own wait (the MCP gateway's wait_ms) and ends the call by leaving.
const applicationsCommandCeiling = 24 * time.Hour

// applicationsCallerTimed reports a call that waits on the application:
// applications@3 Call and Activate. Activate bounds the person's answer and
// the start itself (applicationV3AnswerWait, the readiness deadline).
func applicationsCallerTimed(frame []byte) bool {
	contract, method, err := wire.ServiceMethod(frame)
	return err == nil && contract == "abstraction.facade/applications@3" && (method == "Call" || method == "Activate")
}

// applicationsCallerLeft reports a reply that failed because its caller had
// already closed the connection: an agent helper whose own short wait ended.
// That is the caller's choice, and the host log stays quiet about it.
func applicationsCallerLeft(err error) bool {
	return errors.Is(err, io.ErrClosedPipe) || errors.Is(err, net.ErrClosed) || errors.Is(err, syscall.EPIPE) || errors.Is(err, context.Canceled)
}

// The existing bounded worker retries ordinary storage/native-policy outages
// on its next tick. Cancellation still terminates the one owned worker.
func (h *applicationsHost) runMessageProgress(ticks <-chan time.Time) {
	failing := ""
	for {
		select {
		case <-h.ctx.Done():
			return
		case <-ticks:
			failing = h.messageProgressTick(failing)
		}
	}
}

// messageProgressBudget bounds one background progress pass.
const messageProgressBudget = 2 * time.Second

// messageProgressTick runs one bounded pass. failing is the failure class the
// previous pass reported, or "" after a success. A failure is reported once
// per class until the class changes or a pass succeeds, and the first success
// after a failure is reported once.
func (h *applicationsHost) messageProgressTick(failing string) string {
	work, done := context.WithTimeout(h.ctx, messageProgressBudget)
	err := h.progressMessages(work)
	if err != nil && work.Err() != nil {
		// Owners below map an expired budget to their own unavailable outcome.
		err = errors.Join(err, work.Err())
	}
	done()
	if h.ctx.Err() != nil {
		return failing
	}
	if err == nil {
		if failing != "" && h.report != nil {
			h.report(errors.New("messages: expiry cleanup recovered"))
		}
		return ""
	}
	class := messageProgressFailureClass(err)
	if class != failing && h.report != nil {
		h.report(fmt.Errorf("messages: expiry cleanup unavailable: %s", class))
	}
	return class
}

// messageProgressFailureClass names a progress failure for the host log. The
// progress path builds its errors from fixed text; a file or system call error
// is named by its operation and cause without its paths.
func messageProgressFailureClass(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "pass budget exceeded"
	case errors.Is(err, context.Canceled):
		return "canceled"
	}
	var path *fs.PathError
	if errors.As(err, &path) {
		return "file " + path.Op + ": " + path.Err.Error()
	}
	var link *os.LinkError
	if errors.As(err, &link) {
		return "file " + link.Op + ": " + link.Err.Error()
	}
	var call *os.SyscallError
	if errors.As(err, &call) {
		return "system call " + call.Syscall + ": " + call.Err.Error()
	}
	class := strings.ReplaceAll(err.Error(), "\n", "; ")
	if len(class) > 160 {
		cut := 160
		for cut > 0 && !utf8.RuneStart(class[cut]) {
			cut--
		}
		class = class[:cut]
	}
	return class
}

// applicationsReceiver binds each call's caller from the connection's Program proof.
type applicationsReceiver struct {
	host *applicationsHost
	call *listen.FramedCall
	ctx  context.Context
}

func (r *applicationsReceiver) caller() rights.Subject {
	peer, err := r.call.Peer()
	if err != nil {
		return rights.Subject{}
	}
	subject, err := credservice.SubjectFromPeer(peer)
	if err != nil || subject.Account != r.host.owner || r.call.Recheck() != nil {
		return rights.Subject{}
	}
	return rights.Subject{Account: subject.Account, Program: subject.Program}
}

func (r *applicationsReceiver) Register(v wire.ApplicationDescriptor) (wire.ApplicationChange, error) {
	// The publisher is the code signer of the peer this call bound; the
	// question about a self-registration shows it.
	var publisher asks.Publisher
	if peer, err := r.call.Peer(); err == nil {
		publisher = peerPublisher(peer)
	}
	return r.host.directory.registerFrom(r.ctx, r.caller(), publisher, v), nil
}
func (r *applicationsReceiver) Remove(name string) (wire.ApplicationChange, error) {
	return r.host.directory.remove(r.ctx, r.caller(), name), nil
}
func (r *applicationsReceiver) AskConfirmation(name string) (wire.ApplicationChange, error) {
	return r.host.directory.askConfirmation(r.ctx, r.caller(), name), nil
}
func (r *applicationsReceiver) Announce(v wire.ApplicationPresence) (wire.ApplicationChange, error) {
	defer r.host.reportSlowPresence("announce", time.Now())
	caller := r.caller()
	peer, err := r.call.Peer()
	var session string
	var ok bool
	if err == nil {
		session, ok = applicationSession(peer)
	}
	if !ok {
		caller = rights.Subject{}
		session = ""
	}
	return r.host.directory.announceInSession(r.ctx, caller, session, v), nil
}
func (r *applicationsReceiver) Withdraw(name, instance string) (wire.ApplicationChange, error) {
	caller := r.caller()
	peer, err := r.call.Peer()
	var session string
	var ok bool
	if err == nil {
		session, ok = applicationSession(peer)
	}
	if !ok {
		caller = rights.Subject{}
		session = ""
	}
	return r.host.directory.withdrawInSession(r.ctx, caller, session, name, instance), nil
}
func (r *applicationsReceiver) Observe(cursor string, waitMs int64) (wire.ApplicationPage, error) {
	return r.host.directory.observe(r.ctx, r.caller(), cursor, waitMs), nil
}
func (r *applicationsReceiver) ListStarts(cursor string, limit int64) (wire.ApplicationStartPage, error) {
	return r.host.directory.listStarts(r.ctx, r.caller(), cursor, limit), nil
}
func (r *applicationsReceiver) Activate(application string) (wire.ApplicationActivationResult, error) {
	caller := r.caller()
	peer, err := r.call.Peer()
	var session string
	var ok bool
	if err == nil {
		session, ok = applicationSession(peer)
	}
	if !ok {
		caller = rights.Subject{}
		session = ""
	}
	return r.host.directory.activateInSession(r.ctx, caller, session, application), nil
}
