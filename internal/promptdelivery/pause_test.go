package promptdelivery

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestPauseHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Pause(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context canceled", err)
	}
}
