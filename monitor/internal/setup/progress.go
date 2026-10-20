package setup

import "context"

type progressKey struct{}

// WithProgress gives a long Setup step's context a report of its items done
// and in all, such as the files a removal has checked; the Panel shows it
// beside the running step.
func WithProgress(ctx context.Context, report func(done, total int)) context.Context {
	return context.WithValue(ctx, progressKey{}, report)
}

// reportProgress reports to ctx's WithProgress, when it has one.
func reportProgress(ctx context.Context, done, total int) {
	if report, ok := ctx.Value(progressKey{}).(func(done, total int)); ok && report != nil {
		report(done, total)
	}
}
