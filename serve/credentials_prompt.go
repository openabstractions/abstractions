package main

import (
	"errors"
	"io"
)

// readSecretLine reads bytes up to a line ending, one byte at a time so nothing
// after the line is buffered away, and bounded by the contract's secret size.
func readSecretLine(r io.Reader) ([]byte, error) {
	line := make([]byte, 0, 256)
	one := make([]byte, 1)
	for len(line) <= 4096 {
		n, err := r.Read(one)
		if n == 1 {
			if one[0] == '\n' {
				return line, nil
			}
			line = append(line, one[0])
			continue
		}
		if errors.Is(err, io.EOF) {
			return line, nil
		}
		if err != nil {
			return nil, err
		}
	}
	return nil, errors.New("secret longer than 4096 bytes")
}
