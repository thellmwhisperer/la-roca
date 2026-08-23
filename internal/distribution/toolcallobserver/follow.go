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

// Follow writes each tool call as it lands in the session file. It tails from
// the current end of the file, reading only newly appended complete records,
// with a native file watch waking it sooner than the poll. A truncated or
// rotated file is reopened from the start. Closing the context is a clean stop.
func Follow(ctx context.Context, session Session, out io.Writer, opts FollowOptions) error {
	if opts.PollEvery <= 0 {
		opts.PollEvery = 200 * time.Millisecond
	}
	offset := int64(0)
	if info, err := os.Stat(session.Path); err == nil {
		offset = info.Size()
	} else if !os.IsNotExist(err) {
		return err
	}
	var carry []byte
	emit := func() error {
		info, err := os.Stat(session.Path)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if info.Size() < offset {
			offset = 0
			carry = nil
		}
		file, err := os.Open(session.Path)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		defer file.Close()
		if _, err := file.Seek(offset, io.SeekStart); err != nil {
			return err
		}
		chunk, err := io.ReadAll(file)
		if err != nil {
			return err
		}
		offset += int64(len(chunk))
		buf := append(carry, chunk...)
		last := 0
		for i := 0; i < len(buf); i++ {
			if buf[i] == '\n' {
				last = i + 1
			}
		}
		if last > 0 {
			for _, event := range parsers.ObserveCalls(session.Kind, buf[:last]) {
				line := Format(event)
				if line == "" {
					continue
				}
				if _, err := fmt.Fprintln(out, line); err != nil {
					return err
				}
			}
			carry = append([]byte(nil), buf[last:]...)
		} else {
			carry = append([]byte(nil), buf...)
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
