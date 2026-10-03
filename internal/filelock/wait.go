package filelock

import (
	"context"
	"fmt"
	"os"
	"time"
)

// Mode selects a shared reader lease or an exclusive writer lease.
type Mode bool

const (
	Shared    Mode = false
	Exclusive Mode = true
)

// AcquireContext waits only for real lock contention. It is cancellable and
// makes no FIFO guarantee. onWait runs once, after the first contention.
func AcquireContext(ctx context.Context, path string, mode Mode, onWait func()) (*os.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return nil, err
	}
	waiting := false
	for {
		if err := ctx.Err(); err != nil {
			file.Close()
			return nil, err
		}
		busy, err := tryLock(file, mode)
		if err != nil {
			file.Close()
			return nil, fmt.Errorf("cannot acquire operation lock: %w", err)
		}
		if !busy {
			if err := ctx.Err(); err != nil {
				file.Close()
				return nil, err
			}
			return file, nil
		}
		if !waiting {
			waiting = true
			if onWait != nil {
				onWait()
			}
		}
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			file.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}
