package main

import "context"

// startManagedRepairs starts serial one-time repairs without putting them on the
// listener startup path. The returned channel closes only after every repair exits,
// allowing shutdown to cancel the shared context and join the managed goroutine.
func startManagedRepairs(ctx context.Context, repairs ...func(context.Context)) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		for _, repair := range repairs {
			repair(ctx)
		}
	}()
	return done
}
