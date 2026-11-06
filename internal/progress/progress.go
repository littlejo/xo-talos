// Package progress reports the progress of long transfers on stderr.
package progress

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

// Reporter writes progress lines. On a terminal it redraws one line in place;
// otherwise (logs, CI) it prints a line every Interval. A nil Reporter
// reports nothing.
type Reporter struct {
	out      io.Writer
	tty      bool
	Interval time.Duration
	now      func() time.Time
}

// New returns a Reporter writing to f, redrawing in place if f is a terminal.
func New(f *os.File) *Reporter {
	tty := false
	if info, err := f.Stat(); err == nil {
		tty = info.Mode()&os.ModeCharDevice != 0
	}
	r := &Reporter{out: f, tty: tty, now: time.Now}
	r.Interval = 10 * time.Second
	if tty {
		r.Interval = 200 * time.Millisecond
	}
	return r
}

// Reader counts the bytes read through it.
type Reader struct {
	r     io.Reader
	rep   *Reporter
	label string
	total int64

	mu       sync.Mutex
	done     int64
	start    time.Time
	last     time.Time
	finished bool
}

// Wrap returns a Reader reporting the progress of reading total bytes from r
// (total <= 0 when unknown). Call Finish when the transfer ends.
func (rep *Reporter) Wrap(r io.Reader, total int64, label string) *Reader {
	pr := &Reader{r: r, rep: rep, label: label, total: total}
	if rep != nil {
		pr.start = rep.now()
		pr.last = pr.start
	}
	return pr
}

func (r *Reader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	if r.rep == nil {
		return n, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.done += int64(n)
	if now := r.rep.now(); !r.finished && now.Sub(r.last) >= r.rep.Interval {
		r.last = now
		r.print(now, false)
	}
	return n, err
}

// Finish prints the final line of a transfer. Further calls do nothing.
func (r *Reader) Finish() {
	if r.rep == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.finished {
		return
	}
	r.finished = true
	r.print(r.rep.now(), true)
}

func (r *Reader) print(now time.Time, final bool) {
	var b strings.Builder
	b.WriteString(r.label)
	b.WriteString(": ")
	if r.total > 0 {
		fmt.Fprintf(&b, "%3d%% %s / %s", r.done*100/r.total, Bytes(r.done), Bytes(r.total))
	} else {
		b.WriteString(Bytes(r.done))
	}
	if elapsed := now.Sub(r.start).Seconds(); elapsed > 0 {
		fmt.Fprintf(&b, ", %s/s", Bytes(int64(float64(r.done)/elapsed)))
	}
	if final {
		fmt.Fprintf(&b, " in %s", now.Sub(r.start).Round(time.Second))
	}
	if r.rep.tty {
		// \033[K clears the rest of the previous, possibly longer, line.
		_, _ = fmt.Fprintf(r.rep.out, "\r%s\033[K", b.String())
		if final {
			_, _ = fmt.Fprintln(r.rep.out)
		}
		return
	}
	_, _ = fmt.Fprintln(r.rep.out, b.String())
}

// Bytes formats a byte count with binary units.
func Bytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
