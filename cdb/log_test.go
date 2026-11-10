package cdb

import (
	"context"
	"testing"
)

func TestImpersonatorContext(t *testing.T) {
	if got := ImpersonatorFrom(context.Background()); got != "" {
		t.Errorf("no impersonation: %q", got)
	}
	ctx := WithImpersonator(context.Background(), "admin@example.com")
	child, cancel := context.WithCancel(ctx)
	defer cancel()
	// The handlers derive their contexts from the request's: the mark follows.
	if got := ImpersonatorFrom(child); got != "admin@example.com" {
		t.Errorf("impersonator: %q", got)
	}
}

func TestOriginContext(t *testing.T) {
	if got := OriginFrom(context.Background()); got != (Origin{}) {
		t.Errorf("no origin: %+v", got)
	}
	want := Origin{Name: OriginAssistant, RequestID: "abc"}
	ctx := WithOrigin(context.Background(), want)
	child, cancel := context.WithCancel(ctx)
	defer cancel()
	// The handlers derive their contexts from the request's: the mark follows.
	if got := OriginFrom(child); got != want {
		t.Errorf("origin: %+v", got)
	}
}
