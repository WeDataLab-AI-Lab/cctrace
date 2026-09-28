package syncer

import "context"

func rateLimitContextErr(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, ok := ctx.Deadline(); ok {
		return context.DeadlineExceeded
	}
	return nil
}
