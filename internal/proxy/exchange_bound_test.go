package proxy

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"
)

// Q-NPM-78: an exchange whose caller set no deadline gets one, which runs until the body is closed; one that has a deadline keeps it.
func TestBoundExchange(t *testing.T) {
	t.Run("a context without a deadline gets the bound", func(t *testing.T) {
		ctx, cancel := boundExchange(context.Background(), time.Hour)
		defer cancel()
		d, ok := ctx.Deadline()
		if !ok || time.Until(d) < 59*time.Minute {
			t.Fatalf("deadline %v, %v", d, ok)
		}
	})
	t.Run("a context with a deadline is left as it is", func(t *testing.T) {
		parent, stop := context.WithTimeout(context.Background(), time.Minute)
		defer stop()
		ctx, cancel := boundExchange(parent, time.Hour)
		defer cancel()
		if ctx != parent {
			t.Fatal("the caller's context was replaced")
		}
	})
	t.Run("the bound is not cancelled before the body is closed", func(t *testing.T) {
		ctx, cancel := boundExchange(context.Background(), time.Hour)
		body := &cancelOnClose{ReadCloser: io.NopCloser(strings.NewReader("answer")), cancel: cancel}
		if ctx.Err() != nil {
			t.Fatal("cancelled at once")
		}
		if b, _ := io.ReadAll(body); string(b) != "answer" {
			t.Fatalf("body %q", b)
		}
		_ = body.Close()
		if ctx.Err() == nil {
			t.Fatal("not cancelled by Close")
		}
	})
}
