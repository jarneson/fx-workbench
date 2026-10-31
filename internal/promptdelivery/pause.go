package promptdelivery

import (
	"context"
	"time"
)

const SubmitDelay = 100 * time.Millisecond

func Pause(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
