package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/helios57/familyguard/backend/internal/fgclient"
)

// cmdADB relays a local port to a phone's own adb, through the control plane (FR-19).
//
// Every connection accepted on the local port becomes one stream: adb opens one connection per
// `adb connect` or `adb pair`, so the relay is one stream per adb session rather than one for the
// life of this command. It runs in the foreground until interrupted, so the door it opens is
// visibly open for exactly as long as the terminal holding it.
//
// Nothing about adb is interpreted here, and nothing needs to be: adb's pairing and its connection
// are TLS end to end between the local adb client and the phone's adbd.
func cmdADB(ctx context.Context, env *environment, args []string) error {
	var (
		deviceID string
		target   = fgclient.DebugConnect
		port     int
		listen   = "127.0.0.1:0"
		once     bool
	)
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--pair":
			target = fgclient.DebugPair
		case "--port":
			if i+1 >= len(args) {
				return fmt.Errorf("--port needs the port Wireless debugging shows on the phone")
			}
			n, err := strconv.Atoi(args[i+1])
			if err != nil || n < 1 || n > 65535 {
				return fmt.Errorf("--port must be a number from 1 to 65535, not %q", args[i+1])
			}
			port = n
			i++
		case "--listen":
			if i+1 >= len(args) {
				return fmt.Errorf("--listen needs an address, e.g. --listen 127.0.0.1:15555")
			}
			listen = args[i+1]
			i++
		case "--once":
			once = true
		default:
			if deviceID != "" {
				return fmt.Errorf("unexpected argument %q", args[i])
			}
			deviceID = args[i]
		}
	}
	if deviceID == "" {
		return fmt.Errorf("which device? `fgctl devices` lists them: fgctl adb <device-id>")
	}

	listener, err := net.Listen("tcp", listen)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", listen, err)
	}
	defer listener.Close()
	stop := context.AfterFunc(ctx, func() { listener.Close() })
	defer stop()

	local := listener.Addr().String()
	announce(env, local, deviceID, target)

	// The phone is reached BEFORE adb connects, and one stream is kept ready. adb gives a new
	// connection about ten seconds to answer, and reaching a phone is a command, a wake-up, a sync
	// and a dial — measured 2026-09-23 on an emulator that had just rebooted, `adb connect` gave up
	// with "failed to connect" while the phone had not yet opened its event stream. With a stream
	// waiting, adb's connection is spliced the moment it arrives. It also means a refusal —
	// debugging off, the phone's own reason — is printed as soon as this command starts, not
	// whenever somebody first runs adb.
	prepCtx, stopPreparing := context.WithCancel(ctx)
	defer stopPreparing()
	streams := make(chan *fgclient.DebugStream) // unbuffered: one stream is held until adb takes it
	var (
		prepMu  sync.Mutex
		prepErr error
	)
	go func() {
		for {
			started := time.Now()
			stream, err := env.client.OpenDebugStream(prepCtx, deviceID, target, port)
			if err != nil {
				if prepCtx.Err() == nil {
					prepMu.Lock()
					prepErr = err
					prepMu.Unlock()
					// Ends the accept loop below, which reports this error as the command's own.
					listener.Close()
				}
				return
			}
			fmt.Fprintf(os.Stderr, "fgctl adb: the phone answered after %s; waiting for adb\n",
				time.Since(started).Round(time.Millisecond))
			select {
			case streams <- stream:
			case <-prepCtx.Done():
				stream.Close()
				return
			}
			if once {
				return
			}
		}
	}()

	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		conn, err := listener.Accept()
		if err != nil {
			prepMu.Lock()
			failed := prepErr
			prepMu.Unlock()
			if failed != nil {
				return fmt.Errorf("the phone was not reached: %w", failed)
			}
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("accepting on %s: %w", local, err)
		}
		var stream *fgclient.DebugStream
		select {
		case stream = <-streams:
		case <-ctx.Done():
			conn.Close()
			return nil
		}
		if once {
			// One session and done: the listener closes now, so a second adb connection is
			// refused rather than silently queued behind the first.
			listener.Close()
			relayOne(ctx, conn, stream)
			return nil
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			relayOne(ctx, conn, stream)
		}()
	}
}

// announce says what to type next. On stdout, one line in --json mode, because the caller that
// reads it is often not a person: it is an agent that started this in the background and needs
// the port.
func announce(env *environment, local, deviceID string, target fgclient.DebugTarget) {
	if env.json {
		_ = json.NewEncoder(env.out).Encode(map[string]string{
			"listen": local, "device": deviceID, "target": string(target),
		})
		return
	}
	fmt.Fprintf(env.out, "relaying %s to the phone's adb (%s)\n", local, target)
	if target == fgclient.DebugPair {
		fmt.Fprintf(env.out, "  run: adb pair %s <the six-digit code the phone shows>\n", local)
	} else {
		fmt.Fprintf(env.out, "  run: adb connect %s\n", local)
	}
	fmt.Fprintln(env.out, "  Ctrl-C closes the relay.")
}

func relayOne(ctx context.Context, local net.Conn, stream *fgclient.DebugStream) {
	started := time.Now()
	var once sync.Once
	closeBoth := func() { once.Do(func() { local.Close(); stream.Close() }) }
	stop := context.AfterFunc(ctx, closeBoth)
	defer stop()

	var up, down int64
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); up, _ = io.Copy(stream, local); closeBoth() }()
	go func() { defer wg.Done(); down, _ = io.Copy(local, stream); closeBoth() }()
	wg.Wait()
	fmt.Fprintf(os.Stderr, "fgctl adb: stream closed after %s (%d bytes to the phone, %d from it)\n",
		time.Since(started).Round(time.Second), up, down)
}
