package main

// These are private host scheduling projections. The native facade owner alone
// interprets MCP, validates activation schemas and produces canonical replies.
type panelNativeActivation struct {
	ID      string
	Request []byte
}

type panelNativeModel interface {
	Receive([]byte) ([]panelNativeActivation, [][]byte, error)
	Complete(string, []byte) ([]panelNativeActivation, [][]byte, error)
	Fault(string) ([]panelNativeActivation, [][]byte, error)
	Retire() error
	Close() error
}
