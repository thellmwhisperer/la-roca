package cli

import (
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/distribution/axi"
	"github.com/thellmwhisperer/la-roca/internal/ingest"
)

// ingestRows owns the temporary terminal block shown while sources are read.
// It is erased before the ordered summary is printed, so the moving narration
// becomes one final transcript instead of being repeated above it.
type ingestRows struct {
	out    io.Writer
	active bool

	mu    sync.Mutex
	order []string
	rows  map[string]ingest.SourceProgress
	drawn int
	frame int
	stop  chan struct{}
	done  chan struct{}
}

func newIngestRows(out io.Writer, active bool) *ingestRows {
	r := &ingestRows{out: out, active: active, rows: map[string]ingest.SourceProgress{}}
	if !active {
		return r
	}
	r.stop, r.done = make(chan struct{}), make(chan struct{})
	go r.run()
	return r
}

func (r *ingestRows) update(progress ingest.SourceProgress) {
	if !r.active {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.rows[progress.Source]; !exists {
		r.order = append(r.order, progress.Source)
	}
	r.rows[progress.Source] = progress
}

func (r *ingestRows) run() {
	defer close(r.done)
	ticker := time.NewTicker(spinnerTick)
	defer ticker.Stop()
	for {
		select {
		case <-r.stop:
			return
		case <-ticker.C:
			r.draw()
		}
	}
}

func (r *ingestRows) draw() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.order) == 0 {
		return
	}
	if r.drawn > 0 {
		fmt.Fprintf(r.out, "\x1b[%dA", r.drawn)
	}
	for _, source := range r.order {
		fmt.Fprint(r.out, clearLine)
		fmt.Fprintln(r.out, r.row(r.rows[source]))
	}
	r.drawn = len(r.order)
	r.frame++
}

func (r *ingestRows) row(progress ingest.SourceProgress) string {
	label := ingestSourceLabel(progress.Source)
	if progress.Done {
		return fmt.Sprintf("✓ %s · %s/%s files · %s discarded · %s",
			label, axi.Number(int64(progress.Processed)),
			axi.Number(int64(progress.Total)), axi.Number(int64(progress.Discarded)),
			axi.Duration(progress.ElapsedMS))
	}
	glyph := paint(r.out, ansiCyan, spinnerFrames[r.frame%len(spinnerFrames)])
	return fmt.Sprintf("%s %s · %s/%s files · %s discarded", glyph, label,
		axi.Number(int64(progress.Processed)), axi.Number(int64(progress.Total)),
		axi.Number(int64(progress.Discarded)))
}

func (r *ingestRows) finish() {
	if !r.active {
		return
	}
	close(r.stop)
	<-r.done
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.drawn == 0 {
		return
	}
	fmt.Fprintf(r.out, "\x1b[%dA", r.drawn)
	for index := 0; index < r.drawn; index++ {
		fmt.Fprint(r.out, clearLine)
		if index+1 < r.drawn {
			fmt.Fprintln(r.out)
		}
	}
	if r.drawn > 1 {
		fmt.Fprintf(r.out, "\x1b[%dA", r.drawn-1)
	}
}
