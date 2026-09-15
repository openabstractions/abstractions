// Installed Go job client for the Linux lifecycle fixture.
//
// It selects the installed runtime with bootstrap.SelectInstalled, binds the
// verified facade job client and prints the tokens the C++ and Python clients
// print: IDENTITY, ACCEPTED, UNKNOWN, RECONCILED, NOT_ACCEPTED, RESULT, ERROR.
//
//	go_client submit URL SIZE DIGEST KEY
//	go_client submit-lost URL SIZE DIGEST KEY
//	go_client result KEY EPOCH OPERATION SECONDS
//
// submit-lost sends Submit through the job binding's endpoint and installation
// trust, wrapped by the shared lost-reply helper (conformance/faults/lostreply/go),
// then reconciles the retained identity through the facade JobsClient. The
// helper is fixture code; everything else is what an application writes.
//
// This directory has no go.mod. drive.py copies the file into a module, adds that
// module to the extracted tree's go.work, which also uses the helper module, and
// builds with GOPROXY=off.
package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	request "github.com/openabstractions/abstraction-download/go/abstraction/download/request"
	"github.com/openabstractions/abstraction-facade/go-core/bootstrap"
	client "github.com/openabstractions/abstraction-facade/go/client"
	"github.com/openabstractions/abstraction-identity/listen"
	api "github.com/openabstractions/abstraction-job/go/abstraction/job/acceptance"
	lostreply "github.com/openabstractions/abstractions/conformance/faults/lostreply/go"
)

var reconciliation = []string{"abstraction.job/reconciliation@1"}

func report(label string, result api.AcceptanceResult) {
	if result.Outcome == "accepted" && result.Receipt != nil {
		r := result.Receipt
		fmt.Println(label, r.OperationId, r.LogicalOwner, r.Identity.HistoryEpoch, r.Identity.Key)
		return
	}
	fmt.Println("NOT_ACCEPTED", result.Outcome, result.Reason)
}

func bind(ctx context.Context) (bootstrap.Selection, *client.JobsClient, error) {
	selection, err := bootstrap.SelectInstalled(ctx)
	if err != nil {
		return selection, nil, err
	}
	jobs, err := client.NewVerified(selection.Endpoint, selection.Server).ResolveJobOperations(ctx, client.Requirements{Guarantees: reconciliation})
	return selection, jobs, err
}

func submit(mode string, args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	selection, jobs, err := bind(ctx)
	if err != nil {
		return err
	}
	window, err := jobs.GetHistoryWindow(ctx)
	if err != nil {
		return err
	}
	size, err := strconv.ParseInt(args[1], 10, 64)
	if err != nil {
		return err
	}
	work := &request.Request{Artifact: request.Artifact{Size: size}, Sources: []request.Source{{Scheme: "http", Locator: args[0]}}}
	if args[2] != "-" {
		work.Artifact.Digest = args[2]
	}
	identity := api.RequestIdentity{Key: args[3], HistoryEpoch: window.HistoryEpoch}
	submission := api.Submission{Identity: identity, Kind: "download", Spec: request.Encode(work), RequiredGuarantees: reconciliation}
	fmt.Println("IDENTITY", identity.Key, identity.HistoryEpoch)
	if mode == "submit" {
		result, err := jobs.Submit(ctx, submission)
		if err != nil {
			return err
		}
		report("ACCEPTED", result)
		return nil
	}
	transport := listen.FrameClient{Endpoint: jobs.Binding().Endpoint, Server: &selection.Server, Timeout: 10 * time.Second, MaxFrame: 2 << 20}
	fault := lostreply.New(transport, "Submit")
	if _, err := api.NewRecoverableAcceptanceClient(fault).Submit(submission); err == nil {
		fmt.Println("DELIVERED reply reached the client")
		return nil
	} else {
		fired, discarded := fault.Fired()
		fmt.Println("UNKNOWN go", errors.Is(err, lostreply.ErrLostReply), fired, discarded)
	}
	recovered, err := jobs.Reconcile(ctx, identity)
	if err != nil {
		return err
	}
	report("RECONCILED", recovered)
	return nil
}

func result(args []string) (int, error) {
	seconds, err := strconv.Atoi(args[3])
	if err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(seconds)*time.Second)
	defer cancel()
	_, jobs, err := bind(ctx)
	if err != nil {
		return 0, err
	}
	identity := api.RequestIdentity{Key: args[0], HistoryEpoch: args[1]}
	recovered, err := jobs.Reconcile(ctx, identity)
	if err != nil {
		return 0, err
	}
	report("RECONCILED", recovered)
	if recovered.Outcome != "accepted" || recovered.Receipt.OperationId != args[2] {
		return 4, nil
	}
	for {
		observed, err := jobs.ObserveWork(ctx, identity)
		if err != nil {
			return 0, err
		}
		if observed.Outcome != "observed" || observed.Snapshot == nil {
			return 0, fmt.Errorf("operation unobservable: %s", observed.Outcome)
		}
		if observed.Snapshot.Receipt.OperationId != args[2] {
			return 0, errors.New("operation changed")
		}
		if observed.Snapshot.State == "complete" {
			break
		}
		if observed.Snapshot.State == "failed" || observed.Snapshot.State == "cancelled" {
			fmt.Println("FAILED", observed.Snapshot.State)
			return 5, nil
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	var body bytes.Buffer
	written, err := jobs.CopyResult(ctx, identity, &body)
	if err != nil {
		return 0, err
	}
	if written != int64(body.Len()) {
		return 0, errors.New("copy count differs")
	}
	fmt.Println("RESULT", body.Len(), hex.EncodeToString(body.Bytes()))
	return 0, nil
}

func main() {
	status, err := 2, errors.New("usage: go_client submit|submit-lost URL SIZE DIGEST KEY | result KEY EPOCH OPERATION SECONDS")
	if len(os.Args) == 6 {
		switch os.Args[1] {
		case "submit", "submit-lost":
			status, err = 0, submit(os.Args[1], os.Args[2:])
		case "result":
			status, err = result(os.Args[2:])
		}
	}
	if err != nil {
		fmt.Println("ERROR", err)
		if status == 0 || status == 2 {
			status = 7
		}
	}
	os.Exit(status)
}
