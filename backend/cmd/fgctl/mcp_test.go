package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/helios57/familyguard/backend/internal/fgclient"
)

const listFrame = `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}` + "\n"

const initFrame = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":` +
	`{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}` + "\n"

// lockedBuffer exists because the SDK writes responses from its own goroutine.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (w *lockedBuffer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

// signallingWriter closes seen the first time want appears, so a test can wait for the server to
// have actually answered instead of sleeping and hoping.
type signallingWriter struct {
	lockedBuffer
	want string
	seen chan struct{}
	once sync.Once
}

func (w *signallingWriter) Write(p []byte) (int, error) {
	n, err := w.lockedBuffer.Write(p)
	if strings.Contains(w.String(), w.want) {
		w.once.Do(func() { close(w.seen) })
	}
	return n, err
}

func (w *lockedBuffer) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.String()
}

type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }

// A client stops a stdio MCP server by closing the pipe. The SDK reports that as an error once a
// session exists, and passing it on made every ordinary shutdown exit 1 -- which any supervisor
// reads as a crash. Measured on a Windows guest and reproduced on Linux before this was fixed.
//
// The close has to happen AFTER the server has answered, or the SDK tears the session down before
// it flushes and the "clean exit" is a statement about a server that never started. An earlier
// version of this test fed a string and hit exactly that: err was nil and the output was empty.
func TestRunMCPTreatsAClosedClientAsACleanExit(t *testing.T) {
	in, clientSide := io.Pipe()
	answered := make(chan struct{})
	out := &signallingWriter{want: `"serverInfo"`, seen: answered}

	go func() {
		if _, err := io.WriteString(clientSide, initFrame); err != nil {
			t.Errorf("writing the initialize frame: %v", err)
		}
		select {
		case <-answered:
		case <-time.After(10 * time.Second):
			t.Errorf("the server never answered initialize")
		}
		// A second request, then close WITHOUT waiting for its answer. Closing after the server has
		// gone quiet is not the case that breaks: the SDK only reports a shutdown error when the
		// client disappears with work in flight, which is what a client being killed looks like.
		if _, err := io.WriteString(clientSide, listFrame); err != nil {
			t.Errorf("writing the tools/list frame: %v", err)
		}
		clientSide.Close()
	}()

	err := runMCP(context.Background(), fgclient.New("https://example.invalid", "fgk_unused"), in, out)
	if err != nil {
		t.Fatalf("closing the client pipe is a normal shutdown, but runMCP returned: %v", err)
	}
	if !strings.Contains(out.String(), `"serverInfo"`) {
		t.Fatalf("no session was ever established, so the clean exit above proves nothing:\n%s", out.String())
	}
}

// The negative control for the test above: if runMCP swallowed every error, that test would pass
// and mean nothing. A stream that fails rather than ending must still be reported.
func TestRunMCPStillReportsATransportFailure(t *testing.T) {
	var out lockedBuffer
	boom := errors.New("the pipe broke")
	// MultiReader consumes the first reader's io.EOF itself and does not pass it on, so the
	// watcher sees data and then boom -- never EOF. That is the distinction under test.
	stream := io.MultiReader(strings.NewReader(initFrame), errReader{boom})
	err := runMCP(context.Background(), fgclient.New("https://example.invalid", "fgk_unused"), stream, &out)
	if err == nil {
		t.Fatal("a transport failure was reported as a clean shutdown")
	}
}
