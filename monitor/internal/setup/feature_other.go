//go:build !windows

package setup

import (
	"context"
	"errors"
	"runtime"
)

func queryLemonadeFeature() (FeatureStatus, string, error) {
	return FeatureStatus{Reason: "The OpenAbstractions Lemonade Image feature requires Windows x64"}, "", nil
}

func queryEngineFeature(engine AppID) (FeatureStatus, string, error) {
	if engine != Lemonade && engine != TheNoise && engine != PyTorch {
		return FeatureStatus{}, "", errors.New("unknown installed engine feature")
	}
	return FeatureStatus{Reason: "The selected local image engine requires Windows x64"}, "", nil
}

func queryModelHostFeature() (FeatureStatus, string, error) {
	return FeatureStatus{Reason: modelHostFeatureReason(runtime.GOOS), Unsupported: true}, "", nil
}

// modelHostFeatureReason says why this platform offers no ModelHost: macOS
// runs no native provider in 0.4.0, and every other platform here has no
// pinned engine.
func modelHostFeatureReason(goos string) string {
	if goos == "darwin" {
		return "ModelHost is not available on this Mac: native providers are not available on macOS, because their caller check needs an XPC endpoint"
	}
	return "The OpenAbstractions ModelHost feature requires Windows x64"
}

func addModelHostFeature(context.Context) (FeatureStatus, error) {
	status, _, _ := queryModelHostFeature()
	return status, errors.New(status.Reason)
}
