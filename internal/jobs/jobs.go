// Package jobs runs the single background worker: 24h timeouts, unclaimed
// class countdowns/cleanup, TTL purges and WAL checkpoints.
package jobs

import (
	"context"
	"log/slog"
	"time"

	"leavepass/internal/app"
)

// RunOnce executes the 30-second batch exactly once.
func RunOnce(ctx context.Context, s *app.Store) {
	if n, err := s.TimeoutLeaves(ctx, 200); err != nil {
		slog.Error("timeout leaves", "err", err)
	} else if n > 0 {
		slog.Info("timeout leaves", "count", n)
	}
	if err := s.UnclaimedTick(ctx); err != nil {
		slog.Error("unclaimed tick", "err", err)
	}
	if _, err := s.ExpireResetRequests(ctx); err != nil {
		slog.Error("expire reset requests", "err", err)
	}
	if n := s.RunTTL(ctx); n > 0 {
		slog.Info("ttl purge", "rows", n)
	}
}

// Run starts the worker loop. It returns when ctx is cancelled.
func Run(ctx context.Context, s *app.Store) {
	RunOnce(ctx, s)
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	wal := time.NewTicker(10 * time.Minute)
	defer wal.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			RunOnce(ctx, s)
		case <-wal.C:
			s.CheckpointWAL(ctx)
		}
	}
}
