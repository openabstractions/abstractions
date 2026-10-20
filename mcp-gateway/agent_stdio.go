package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// agentStdioLineLimit is the longest stdin line the helper accepts, as the
// SDK transport's MaxLineLength was.
const agentStdioLineLimit = 1 << 20

// guardStdioLines passes the host's stdin to the MCP transport one whole
// JSON-RPC line at a time. A line that is not a JSON-RPC message, or a batch
// of them, or that is longer than the limit, is dropped and reported on
// stderr. The SDK's reader ends the whole connection on one such line; the
// helper keeps serving for the life of stdio instead. EOF passes through.
func guardStdioLines(in io.Reader, report func(string)) io.ReadCloser {
	reader, writer := io.Pipe()
	go func() {
		writer.CloseWithError(copyJSONRPCLines(in, writer, report))
	}()
	return reader
}

func copyJSONRPCLines(in io.Reader, out io.Writer, report func(string)) error {
	lines := bufio.NewReaderSize(in, 64<<10)
	var line []byte
	for {
		chunk, err := lines.ReadSlice('\n')
		if len(line)+len(chunk) <= agentStdioLineLimit+1 {
			line = append(line, chunk...)
		} else {
			line = line[:0]
			if report != nil {
				report("a stdin line longer than the limit was dropped")
			}
			for err == bufio.ErrBufferFull {
				_, err = lines.ReadSlice('\n')
			}
			if err == nil {
				continue
			}
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if len(bytes.TrimSpace(line)) != 0 {
			if validJSONRPCLine(line) {
				// The SDK accepts only a newline after the JSON value. Strip
				// host whitespace after validating the entire line.
				if _, werr := out.Write(append(bytes.TrimSpace(line), '\n')); werr != nil {
					return nil
				}
			} else if report != nil {
				report("a stdin line that is not a JSON-RPC message was dropped")
			}
		}
		line = line[:0]
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func validJSONRPCLine(line []byte) bool {
	line = bytes.TrimSpace(line)
	// DecodeMessage accepts a JSON prefix. Unmarshal first ensures there is
	// exactly one complete value. Batches are unsupported by current MCP
	// versions and would terminate the SDK connection.
	var value json.RawMessage
	if json.Unmarshal(line, &value) != nil || len(line) == 0 || line[0] != '{' {
		return false
	}
	_, err := jsonrpc.DecodeMessage(line)
	return err == nil
}

// guardedStdioTransport is the helper's stdio: stdin through the line guard,
// stdout unchanged and never closed by the transport. A dedicated helper's
// stdio can hand the connection to a successor (agent_handover.go).
func guardedStdioTransport() *mcp.IOTransport {
	if s := currentProcessAgentStdio(); s != nil {
		return s.transport()
	}
	return &mcp.IOTransport{Reader: guardStdioLines(os.Stdin, reportStdioLine), Writer: stdoutCloser{os.Stdout}, MaxLineLength: agentStdioLineLimit + 1}
}

type stdoutCloser struct{ io.Writer }

func (stdoutCloser) Close() error { return nil }

func reportStdioLine(problem string) {
	//unchecked: diagnostic for a dropped host line; stderr has no fallback destination
	fmt.Fprintf(os.Stderr, "openabstractions-mcp pid=%d: %s\n", os.Getpid(), problem)
}
