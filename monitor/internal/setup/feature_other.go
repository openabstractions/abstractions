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

func addLemonadeFeature(context.Context) (FeatureStatus, error) {
	status, _, _ := queryLemonadeFeature()
	return status, errors.New(status.Reason)
}

func queryEngineFeature(engine AppID) (FeatureStatus, string, error) {
	if engine != Lemonade && engine != TheNoise && engine != PyTorch {
		return FeatureStatus{}, "", errors.New("unknown installed engine feature")
	}
	return FeatureStatus{Reason: "The selected local image engine requires Windows x64"}, "", nil
}

func addEngineFeature(_ context.Context, engine AppID) (FeatureStatus, error) {
	status, _, err := queryEngineFeature(engine)
	if err != nil {
		return status, err
	}
	return status, errors.New(status.Reason)
}

func queryModelHostFeature() (FeatureStatus, string, error) {
	return FeatureStatus{Reason: modelHostFeatureReason(runtime.GOOS)}, "", nil
}

// modelHostFeatureReason says why this platform offers no ModelHost: macOS
// runs no native provider in 0.4.0, and every other platform here has no
// pinned engine.
func modelHostFeatureReason(goos string) string {
	if goos == "darwin" {
		return "ModelHost is not available on this Mac: native providers are not available on macOS in 0.4.0, because their caller check needs an XPC endpoint"
	}
	return "The OpenAbstractions ModelHost feature requires Windows x64"
}

func addModelHostFeature(context.Context) (FeatureStatus, error) {
	status, _, _ := queryModelHostFeature()
	return status, errors.New(status.Reason)
}
