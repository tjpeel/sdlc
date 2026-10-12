package workrun

import (
	"bytes"

	"github.com/tjpeel/sdlc/internal/runusage"
)

// Retain boundary snapshots while native work is running, so a crash does not
// erase the already captured baseline. Other usage retains its final checkpoint.
type subscriptionRecorder struct {
	observer *runusage.Observer
	save     func(runusage.Usage) error
	revision uint64
}

func (recorder *subscriptionRecorder) Write(data []byte) (int, error) {
	written := 0
	for len(data) > 0 {
		end := bytes.IndexByte(data, '\n')
		if end < 0 {
			end = len(data)
		} else {
			end++
		}
		n, err := recorder.write(data[:end])
		written += n
		if err != nil {
			return written, err
		}
		data = data[end:]
	}
	return written, nil
}

func (recorder *subscriptionRecorder) write(data []byte) (int, error) {
	n, err := recorder.observer.Write(data)
	if err != nil {
		return n, err
	}
	revision := recorder.observer.SubscriptionRevision()
	if revision != recorder.revision {
		if err := recorder.save(recorder.observer.Snapshot()); err != nil {
			return n, err
		}
		recorder.revision = revision
	}
	return n, nil
}
