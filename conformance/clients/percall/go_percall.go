// Time N identical caller@1 Observe calls from one Go process at a runtime
// resolver endpoint through listen.FrameClient: single opens a connection per
// call, session keeps one (FRAMING.md "Sessions").
//
//	go_percall <runtime endpoint> <calls> <warmup> single|session
//
// Prints one PERCALL line; see percall/README.md.
package main

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go-core/go/abstraction/facade"
	"github.com/openabstractions/abstraction-identity/listen"
)

func main() {
	if len(os.Args) != 5 || (os.Args[4] != "single" && os.Args[4] != "session") {
		fmt.Println("go_percall <runtime endpoint> <calls> <warmup> single|session")
		if len(os.Args) == 2 && os.Args[1] == "--help" {
			return
		}
		os.Exit(2)
	}
	calls, err1 := strconv.Atoi(os.Args[2])
	warmup, err2 := strconv.Atoi(os.Args[3])
	if err1 != nil || err2 != nil || calls < 1 || warmup < 0 {
		fmt.Fprintln(os.Stderr, "calls must be positive and warmup non-negative")
		os.Exit(2)
	}
	session := os.Args[4] == "session"
	client := wire.NewCallerClient(listen.FrameClient{Endpoint: os.Args[1], Timeout: 10 * time.Second, Sessions: session})
	var first float64
	var code string
	samples := make([]float64, 0, calls)
	for i := 0; i < warmup+calls; i++ {
		began := stamp()
		observed, err := client.Observe()
		took := float64(stamp()-began) / float64(time.Millisecond)
		if err != nil {
			fmt.Fprintln(os.Stderr, "observe:", err)
			os.Exit(1)
		}
		if observed.Outcome != wire.CallerOutcomeObserved {
			fmt.Fprintln(os.Stderr, "observe outcome:", observed.Outcome)
			os.Exit(1)
		}
		if i == 0 {
			first = took
			for _, a := range observed.Attributes {
				if a.Attribute == "code" {
					code = a.Proof
				}
			}
		}
		if i >= warmup {
			samples = append(samples, took)
		}
	}
	name := "go"
	if session {
		name = "go-session"
	}
	fmt.Println(line(name, first, samples, code))
}

func line(client string, first float64, samples []float64, code string) string {
	sort.Float64s(samples)
	rank := func(p float64) float64 {
		i := int(float64(len(samples))*p+0.999999) - 1
		if i < 0 {
			i = 0
		}
		return samples[i]
	}
	return strings.Join([]string{"PERCALL", client, "calls=" + strconv.Itoa(len(samples)),
		fmt.Sprintf("first=%.3f", first), fmt.Sprintf("p50=%.3f", rank(0.5)), fmt.Sprintf("p90=%.3f", rank(0.9)),
		fmt.Sprintf("p99=%.3f", rank(0.99)), fmt.Sprintf("max=%.3f", samples[len(samples)-1]), "code=" + code}, " ")
}
