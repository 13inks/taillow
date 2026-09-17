package audit

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// lines splits a JSONL buffer and decodes every line, failing on a bad one.
func lines(t *testing.T, raw string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, l := range strings.Split(strings.TrimSuffix(raw, "\n"), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("line %q is not JSON: %v", l, err)
		}
		out = append(out, m)
	}
	return out
}

func TestWriteOneLinePerEntry(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf)
	pacific := time.FixedZone("PDT", -7*3600)
	when := time.Date(2026, 9, 17, 9, 30, 0, 0, pacific)

	if err := l.Write(Entry{Time: when, Caller: "alice@example.com", Node: "alice-laptop", Model: "m", InputTokens: 7, OutputTokens: 5, Decision: "allowed", Status: 200}); err != nil {
		t.Fatal(err)
	}
	if err := l.Write(Entry{Caller: "unknown", Decision: "refused_identity", Status: 403, Reason: "why"}); err != nil {
		t.Fatal(err)
	}

	if n := strings.Count(buf.String(), "\n"); n != 2 || !strings.HasSuffix(buf.String(), "\n") {
		t.Fatalf("want 2 newline-terminated lines, got %q", buf.String())
	}
	got := lines(t, buf.String())
	if got[0]["time"] != "2026-09-17T16:30:00Z" {
		t.Errorf("time = %v, want it converted to UTC", got[0]["time"])
	}
	if got[0]["inputTokens"] != float64(7) || got[0]["outputTokens"] != float64(5) || got[0]["status"] != float64(200) {
		t.Errorf("counts or status wrong: %v", got[0])
	}
	if _, ok := got[0]["reason"]; ok {
		t.Errorf("empty reason should be omitted: %v", got[0])
	}
	if _, ok := got[1]["model"]; ok {
		t.Errorf("empty model should be omitted: %v", got[1])
	}
	stamped, err := time.Parse(time.RFC3339Nano, got[1]["time"].(string))
	if err != nil || time.Since(stamped) > time.Minute || stamped.Location() != time.UTC {
		t.Errorf("a zero time should be stamped with now in UTC, got %v (%v)", got[1]["time"], err)
	}
}

func TestWriteConcurrentLinesDoNotInterleave(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf)
	const n = 50
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := l.Write(Entry{Caller: strings.Repeat("c", i+1), Decision: "allowed"}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()

	// Order is not promised. Every line being whole, and every writer being
	// present exactly once, is.
	seen := make(map[string]bool)
	for _, m := range lines(t, buf.String()) {
		seen[m["caller"].(string)] = true
	}
	if len(seen) != n {
		t.Errorf("got %d distinct whole lines, want %d", len(seen), n)
	}
}

func TestOpenAppendsAndNeverTruncates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	for _, caller := range []string{"first", "second"} {
		l, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := l.Write(Entry{Caller: caller}); err != nil {
			t.Fatal(err)
		}
		if err := l.Close(); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := lines(t, string(raw))
	if len(got) != 2 || got[0]["caller"] != "first" || got[1]["caller"] != "second" {
		t.Errorf("reopening lost or reordered lines: %v", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %o, want 600: the log names who asked what", perm)
	}
}

func TestOpenFailureNamesThePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "no-such-dir", "audit.jsonl")
	_, err := Open(path)
	if !errors.Is(err, fs.ErrNotExist) || !strings.Contains(err.Error(), path) {
		t.Errorf("err = %v, want fs.ErrNotExist wrapped with the path", err)
	}
}

func TestWriteAfterCloseFails(t *testing.T) {
	l, err := Open(filepath.Join(t.TempDir(), "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if err := l.Write(Entry{Caller: "late"}); err == nil {
		t.Error("Write after Close returned nil; a lost audit line must be an error")
	}
}

type stubWriter struct {
	short bool
	err   error
}

func (s stubWriter) Write(p []byte) (int, error) {
	if s.short {
		return len(p) - 1, nil
	}
	return 0, s.err
}

func TestWriteReportsWriterFaults(t *testing.T) {
	boom := errors.New("disk full")
	if err := New(stubWriter{err: boom}).Write(Entry{}); !errors.Is(err, boom) {
		t.Errorf("writer error: got %v, want it wrapped", err)
	}
	if err := New(stubWriter{short: true}).Write(Entry{}); !errors.Is(err, io.ErrShortWrite) {
		t.Errorf("short write: got %v, want io.ErrShortWrite", err)
	}
}

func TestCloseLeavesABorrowedWriterAlone(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf)
	if err := l.Close(); err != nil {
		t.Errorf("Close = %v, want nil", err)
	}
	if err := l.Write(Entry{Caller: "still-open"}); err != nil {
		t.Errorf("Write after Close on a borrowed writer = %v, want nil", err)
	}
}
