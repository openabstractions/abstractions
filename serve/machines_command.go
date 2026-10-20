package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	mwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/machines"
	"github.com/openabstractions/abstraction-facade/go/client"
)

const machinesUsage = `Usage: openabstractions machines <list|offer|enter|observe|cancel|remove> [options]

Local native-operator pairing control. Final consent is supplied in the runtime's
native question on both PCs. These draft scope/lifetime choices await owner approval.
Every command accepts --endpoint, --runtime-program, --timeout and --json.

offer/enter require explicit --request none|one_host_inference, --offer
none|one_host_inference and --binding-name NAME. A nonempty offer also requires
--offer-host NAME and --normal-service-address NUMERIC_IP:PORT. offer requires
--invitation-address NUMERIC_IP:PORT. No listener or permission is selected by default.
enter reads one invitation line from stdin; never put the code in argv or an environment.
offer writes its one-use invitation to stdout. Keep that output private.

observe OPERATION [--after CURSOR] [--wait 0..30s]; cancel OPERATION;
remove PEER_KEY --revision REVISION. list reads one bounded page (--cursor CURSOR).
A lost Enter/offer reply is uncertain: observe the exact saved operation; never redial.
The first nonempty scope includes completion (including embeddings) on the selected
host-name slot, account-wide router inventory, and remote service admission until revoked.
`

func machinesCommand(args []string, input io.Reader, output, diagnostics io.Writer) error {
	if len(args) == 0 || containsHelp(args) {
		_, err := io.WriteString(output, machinesUsage)
		return err
	}
	verb := args[0]
	switch verb {
	case "list", "offer", "enter", "observe", "cancel", "remove":
	default:
		return commandMistake(diagnostics, "machines: unknown command", "openabstractions machines --help")
	}
	flags := flag.NewFlagSet("machines "+verb, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var options serviceOptions
	options.bind(flags)
	request := flags.String("request", "", "explicit receiving scope requested from the other PC")
	offer := flags.String("offer", "", "explicit local receiving scope offered to the other PC")
	host := flags.String("offer-host", "", "currently registered local host-name slot")
	service := flags.String("normal-service-address", "", "explicit normal receiving numeric address")
	binding := flags.String("binding-name", "", "local name mapped to the exact peer key")
	address := flags.String("invitation-address", "", "explicit one-shot invitation numeric address")
	cursor := flags.String("cursor", "", "list cursor")
	after := flags.String("after", "", "observation cursor")
	revision := flags.String("revision", "", "exact current pairing revision")
	wait := flags.Duration("wait", 0, "observation wait up to 30s")
	pos, err := parsePositional(flags, args[1:])
	if err != nil {
		return badFlag(flags, diagnostics, "machines "+verb, machinesUsage, args[1:], err)
	}
	if options.budget < 0 || *wait < 0 || *wait > 30*time.Second {
		return commandMistake(diagnostics, "machines: invalid time budget", "openabstractions machines --help")
	}
	need := 0
	if verb == "observe" || verb == "cancel" || verb == "remove" {
		need = 1
	}
	if len(pos) != need {
		return commandMistake(diagnostics, "machines: incorrect argument count", "openabstractions machines --help")
	}
	var preparation mwire.MachinePreparation
	var code string
	if verb == "offer" || verb == "enter" {
		kind := func(s string) (mwire.MachineScopeKind, bool) {
			switch s {
			case "none":
				return mwire.MachineScopeKindNone, true
			case "one_host_inference":
				return mwire.MachineScopeKindOneHostInference, true
			}
			return 0, false
		}
		r, ok := kind(*request)
		o, offered := kind(*offer)
		if !ok || !offered || !providerName.MatchString(*binding) {
			return commandMistake(diagnostics, "machines: explicit scopes and binding-name are required", "openabstractions machines --help")
		}
		preparation = mwire.MachinePreparation{Request: r, Offer: o, OfferHost: *host, NormalServiceAddress: *service, BindingName: *binding}
		if verb == "enter" {
			data, e := io.ReadAll(io.LimitReader(input, 76))
			if e != nil {
				return fmt.Errorf("machines: invitation input unavailable")
			}
			code = strings.TrimSuffix(strings.TrimSuffix(string(data), "\n"), "\r")
			clear(data)
			if _, _, e = decodePairingInvitation(code); e != nil {
				return fmt.Errorf("machines: invalid invitation input")
			}
		}
	}
	if options.budget == 0 {
		options.budget = callBudget
	}
	w := newWaiting(options.budget)
	defer w.stop()
	machine, source, err := options.machine(diagnostics, "machines "+verb, machinesUsage)
	if err != nil {
		return err
	}
	control, err := machine.ResolveMachineControl(w.ctx, client.Requirements{Scope: client.ScopeLocal})
	if err != nil {
		return notResolved("machines", source, err)
	}
	var result any
	switch verb {
	case "list":
		result, err = control.List(w.ctx, *cursor, 64)
	case "observe":
		result, err = control.Observe(w.ctx, pos[0], *after, *wait)
	case "cancel":
		result, err = control.Cancel(w.ctx, pos[0])
	case "remove":
		result, err = control.Remove(w.ctx, *revision, pos[0])
	case "offer", "enter":
		page, e := control.List(w.ctx, "", 1)
		if e != nil {
			return e
		}
		if page.Outcome != mwire.MachineListOutcomePage {
			return fmt.Errorf("machines: preparation unavailable")
		}
		nonce, e := randomPairingHex()
		if e != nil {
			return e
		}
		id := mwire.MachineRequestIdentity{Epoch: page.Epoch, ExpiresMs: page.RequestExpiresMs, Nonce: nonce}
		if _, err = fmt.Fprintln(diagnostics, "Pairing operation:", id.Epoch+"."+id.Nonce); err != nil {
			return err
		}
		if verb == "offer" {
			result, err = control.StartOffer(w.ctx, id, *address, preparation)
		} else {
			result, err = control.Enter(w.ctx, id, code, preparation)
		}
	}
	if err != nil {
		return fmt.Errorf("machines: call result unavailable; inspect the existing operation before any new attempt")
	}
	return json.NewEncoder(output).Encode(result)
}
