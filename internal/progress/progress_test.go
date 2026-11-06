package progress

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"
)

// fakeClock advances by step on every call.
func fakeClock(step time.Duration) func() time.Time {
	t := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	return func() time.Time {
		t = t.Add(step)
		return t
	}
}

func TestLogLines(t *testing.T) {
	var out bytes.Buffer
	rep := &Reporter{out: &out, Interval: 10 * time.Second, now: fakeClock(5 * time.Second)}
	r := rep.Wrap(bytes.NewReader(make([]byte, 4096)), 4096, "upload")
	buf := make([]byte, 1024)
	for {
		if _, err := r.Read(buf); err == io.EOF {
			break
		}
	}
	r.Finish()
	r.Finish()

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 2 periodic + 1 final:\n%s", len(lines), out.String())
	}
	if !strings.HasPrefix(lines[0], "upload:  50% 2.0 KiB / 4.0 KiB") {
		t.Errorf("first line %q", lines[0])
	}
	if !strings.HasPrefix(lines[1], "upload: 100%") || strings.Contains(lines[1], " in ") {
		t.Errorf("second line %q", lines[1])
	}
	if !strings.HasPrefix(lines[2], "upload: 100% 4.0 KiB / 4.0 KiB") || !strings.Contains(lines[2], " in ") {
		t.Errorf("final line %q", lines[2])
	}
	if strings.Contains(out.String(), "\r") {
		t.Error("log output contains carriage returns")
	}
}

func TestTerminalRedrawsInPlace(t *testing.T) {
	var out bytes.Buffer
	rep := &Reporter{out: &out, tty: true, Interval: time.Nanosecond, now: fakeClock(time.Second)}
	r := rep.Wrap(strings.NewReader("abcdef"), 0, "scan")
	_, _ = io.Copy(io.Discard, io.LimitReader(r, 3))
	_, _ = io.Copy(io.Discard, r)
	r.Finish()
	s := out.String()
	if !strings.HasPrefix(s, "\rscan: ") || strings.Count(s, "\n") != 1 || !strings.HasSuffix(s, "\n") {
		t.Fatalf("unexpected terminal output %q", s)
	}
	if strings.Contains(s, "%") {
		t.Fatalf("percentage shown for an unknown total: %q", s)
	}
}

func TestNilReporter(t *testing.T) {
	var rep *Reporter
	r := rep.Wrap(strings.NewReader("data"), 4, "x")
	b, err := io.ReadAll(r)
	if err != nil || string(b) != "data" {
		t.Fatalf("ReadAll = %q, %v", b, err)
	}
	r.Finish()
}

func TestBytes(t *testing.T) {
	for n, want := range map[int64]string{512: "512 B", 2048: "2.0 KiB", 4453302272: "4.1 GiB"} {
		if got := Bytes(n); got != want {
			t.Errorf("Bytes(%d) = %q, want %q", n, got, want)
		}
	}
}
