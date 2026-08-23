package toolcallobserver

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/thellmwhisperer/la-roca/pkg/parsers"
)

// FollowOptions controls how the live tail notices new bytes.
type FollowOptions struct {
	PollEvery time.Duration
}

// Follow writes each tool call as it lands in the session file. It polls, and
// on supported platforms a native file watch wakes it sooner. Closing the
// context is a clean stop.
func Follow(ctx context.Context, session Session, out io.Writer, opts FollowOptions) error {
	if opts.PollEvery <= 0 {
		opts.PollEvery = 200 * time.Millisecond
	}
	seen := map[string]bool{}
	emit := func() error {
		data, err := os.ReadFile(session.Path)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		for _, event := range parsers.ObserveCalls(session.Kind, data) {
			key := event.ID + "\x00" + event.Timestamp + "\x00" + event.Name + "\x00" + boolKey(event.IsResult)
			if seen[key] {
				continue
			}
			seen[key] = true
			line := Format(event)
			if line == "" {
				continue
			}
			if _, err := fmt.Fprintln(out, line); err != nil {
				return err
			}
		}
		return nil
	}
	if err := emit(); err != nil {
		return err
	}
	changes := make(chan struct{}, 1)
	stopWatch := watchFile(session.Path, changes)
	defer stopWatch()
	ticker := time.NewTicker(opts.PollEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changes:
			if err := emit(); err != nil {
				return err
			}
		case <-ticker.C:
			if err := emit(); err != nil {
				return err
			}
		}
	}
}

func boolKey(value bool) string {
	if value {
		return "1"
	}
	return "0"
}
