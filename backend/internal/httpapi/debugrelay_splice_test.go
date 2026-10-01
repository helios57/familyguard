package httpapi

import (
	"context"
	"io"
	"net"
	"testing"
	"time"
)

// spliced runs splice between a parent and a phone made of in-memory pipes and returns the two far
// ends a test drives, and a channel closed when splice returns. Both far ends are drained, so a
// write is never held up by nobody reading — the pipes are synchronous.
func spliced(t *testing.T, ceiling, idle time.Duration) (parent, phone net.Conn, done <-chan struct{}) {
	t.Helper()
	parentFar, parentNear := net.Pipe()
	phoneFar, phoneNear := net.Pipe()
	t.Cleanup(func() { parentFar.Close(); phoneFar.Close() })
	go func() { _, _ = io.Copy(io.Discard, parentFar) }()
	go func() { _, _ = io.Copy(io.Discard, phoneFar) }()
	ch := make(chan struct{})
	go func() {
		splice(context.Background(), debugLeg{parentNear, parentNear}, debugLeg{phoneNear, phoneNear}, ceiling, idle)
		close(ch)
	}()
	return parentFar, phoneFar, ch
}

// FR-19.6: "A session ends after an hour idle". The relay used to leave that to ingress-nginx's
// proxy-read-timeout, so a deployment without that proxy — any self-hosted one — kept an idle adb
// door open on a child's phone for the full four hours.
func TestSpliceEndsAnIdleSession(t *testing.T) {
	parent, _, done := spliced(t, 10*time.Second, 300*time.Millisecond)
	start := time.Now()
	if _, err := parent.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
		if elapsed := time.Since(start); elapsed < 250*time.Millisecond {
			t.Fatalf("the session ended after %v, before it had been idle for the window", elapsed)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a session with no bytes for 300 ms of a 300 ms idle window is still open after 5 s")
	}
}

// A session nothing ever crosses — adb connected and then left — is idle from its first instant.
func TestSpliceEndsASessionThatNeverCarriedAByte(t *testing.T) {
	_, _, done := spliced(t, 10*time.Second, 300*time.Millisecond)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a session that never carried a byte is still open after 5 s of a 300 ms idle window")
	}
}

// The idle window restarts on every byte, in either direction: a session in use is not idle.
func TestSpliceKeepsABusySessionOpen(t *testing.T) {
	parent, phone, done := spliced(t, 10*time.Second, 300*time.Millisecond)
	for i := 0; i < 12; i++ { // 1.2 s of traffic, four idle windows' worth
		conn := parent
		if i%2 == 1 {
			conn = phone
		}
		if _, err := conn.Write([]byte("x")); err != nil {
			t.Fatalf("write %d: the session closed while in use: %v", i, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	select {
	case <-done:
		t.Fatal("a session carrying a byte every 100 ms was closed as idle")
	default:
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("after the traffic stopped, the session never went idle")
	}
}

// "…and after four hours regardless": the ceiling ends a session that is never idle.
func TestSpliceEndsABusySessionAtTheCeiling(t *testing.T) {
	parent, _, done := spliced(t, 400*time.Millisecond, 10*time.Second)
	stop := time.After(5 * time.Second)
	for {
		select {
		case <-done:
			return
		case <-stop:
			t.Fatal("a busy session outlived its ceiling")
		default:
		}
		_, _ = parent.Write([]byte("x"))
		time.Sleep(20 * time.Millisecond)
	}
}
