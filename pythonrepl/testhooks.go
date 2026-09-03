package pythonrepl

import "context"

// testHooks is deliberately package-private and absent from Options so it can
// never affect mounted behavior or component identity. Tests install hooks only
// after canonicalization to force lifecycle race boundaries deterministically.
type testHooks struct {
	afterVenvDirectory    func(context.Context)
	beforeVenvPublish     func(context.Context)
	beforeVenvCreatorWait func()
	removeVenv            func(string) error
	afterSupervisorStart  func(context.Context)
	beforeRequestWrite    func(context.Context)
	afterRequestWrite     func(context.Context)
	beforeResponseCommit  func(context.Context)
	beforeReapAuthorize   func()
}

func runContextHook(hook func(context.Context), ctx context.Context) {
	if hook != nil {
		hook(ctx)
	}
}

func runHook(hook func()) {
	if hook != nil {
		hook()
	}
}
