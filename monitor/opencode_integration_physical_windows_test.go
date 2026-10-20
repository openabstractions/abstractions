package main

import (
	"errors"
	"testing"
)

func TestOpenCodePhysicalPreparationPreservesEnrolledHold(t *testing.T) {
	image := &preparedIntegrationFixture{program: "inert owned helper"}
	calls := []string{}
	got, err := preparePhysicalIntegrationImageWith(
		func() (installerIntegrationImage, error) {
			calls = append(calls, "prepare")
			return nil, errors.New("older image")
		},
		func() (installerIntegrationImage, error) {
			calls = append(calls, "replace")
			return nil, errors.New("already enrolled")
		},
		func() (installerIntegrationImage, error) { calls = append(calls, "hold"); return image, nil })
	if err != nil || got != image || len(calls) != 3 || calls[2] != "hold" {
		t.Fatal(got, calls, err)
	}
}
