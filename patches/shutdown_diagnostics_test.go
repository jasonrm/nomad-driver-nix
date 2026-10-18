package executor

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func receiveEvent(t *testing.T, events <-chan string) string {
	t.Helper()
	select {
	case event := <-events:
		return event
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for shutdown diagnostic")
		return ""
	}
}

func TestObserveSignalShutdownReturns(t *testing.T) {
	for _, shutdownErr := range []error{nil, errors.New("cleanup failed")} {
		t.Run(strings.ReplaceAll("error="+errorText(shutdownErr), " ", "_"), func(t *testing.T) {
			events := make(chan string, 8)
			calls := 0
			observeSignalShutdown(func() error {
				calls++
				return shutdownErr
			}, func(event string) { events <- event }, 50*time.Millisecond)
			if calls != 1 {
				t.Fatalf("cleanup called %d times", calls)
			}
			if event := receiveEvent(t, events); event != "signal received; cleanup starting" {
				t.Fatalf("unexpected initial event: %s", event)
			}
			event := receiveEvent(t, events)
			if !strings.Contains(event, "error="+errorText(shutdownErr)) || !strings.Contains(event, "without exiting executor") {
				t.Fatalf("unexpected completion event: %s", event)
			}
			select {
			case event := <-events:
				t.Fatalf("unexpected watchdog event after completion: %s", event)
			case <-time.After(100 * time.Millisecond):
			}
		})
	}
}

func errorText(err error) string {
	if err == nil {
		return "<nil>"
	}
	return err.Error()
}

func TestObserveSignalShutdownBlockedCleanup(t *testing.T) {
	events := make(chan string, 8)
	release := make(chan struct{})
	finished := make(chan struct{})
	defer func() {
		close(release)
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
			t.Error("cleanup did not return after release")
		}
		if event := receiveEvent(t, events); !strings.Contains(event, "cleanup returned") {
			t.Errorf("expected completion event, got %s", event)
		}
	}()
	go func() {
		observeSignalShutdown(func() error {
			<-release
			return nil
		}, func(event string) { events <- event }, 10*time.Millisecond)
		close(finished)
	}()
	receiveEvent(t, events)
	if event := receiveEvent(t, events); !strings.Contains(event, "cleanup has not returned") {
		t.Fatalf("expected watchdog event, got %s", event)
	}
	select {
	case <-finished:
		t.Fatal("diagnostics changed blocking cleanup behavior")
	default:
	}
}
