package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/helios57/familyguard/backend/internal/store"
)

// Remote adb (FR-19): a parent reaches the phone's own Wireless debugging port from anywhere.
//
// The server is a relay and nothing else (FR-19.4). It never speaks adb, never sees a key, and cannot read
// what it carries: adb's pairing and its connection are both TLS between the parent's adb client
// and the phone's adbd, and what passes through here is two byte streams spliced end to end.
//
// A session is two HTTP requests that each become a raw stream, and a command between them:
//
//  1. the parent's leg: `GET /api/v1/devices/:id/debug`, which queues OPEN_DEBUG_STREAM and waits;
//  2. the phone fetches the command, connects to its own adbd, and dials the second leg,
//     `GET /api/v1/device/debug/:stream`;
//  3. both are answered 101 and spliced.
//
// Upgrade rather than a WebSocket, because nothing here has frames to carry: adb is already a
// stream, and the only thing a WebSocket would add is a framing layer on both ends and a library on
// the phone. ingress-nginx forwards any Upgrade token, not only "websocket".
//
// **The gate is the child's `allow_debugging`, and it is checked on every open.** That switch is
// what lifts `no_debugging_features` on the phone; without it adbd is not running and there is
// nothing to reach. Checking it here as well means a parent gets a sentence instead of a timeout,
// and the phone checks it a third time against the policy it has actually applied.

const (
	// debugUpgrade is the protocol token both legs ask for. A request without it is refused with
	// 426 before anything is queued, so a browser or a curl that stumbles on the path costs nothing.
	debugUpgrade = "familyguard-debug"

	// debugDialWindow is how long the parent's leg waits for the phone to dial back. The command
	// reaches an awake phone over its event stream in about a second; what this covers is the
	// phone's own work — finding the adb port and connecting to it — plus a sync it may be in the
	// middle of.
	debugDialWindow = 30 * time.Second

	// debugCommandTTL outlives the dial window by a margin and no more. A phone that comes online
	// an hour later must not dial a stream whose parent gave up long ago; it would find nothing
	// waiting (404) and report a failure that describes nobody's request.
	debugCommandTTL = 45 * time.Second

	// debugMaxSession bounds one stream however busy it is (FR-19.6). ingress-nginx already cuts one that is
	// idle for an hour (proxy-read-timeout); this is the ceiling on one that never goes idle, so a
	// forgotten `adb logcat` does not hold a door open on a child's phone for days.
	debugMaxSession = 4 * time.Hour
)

// debugLeg is one side of a stream after its 101: the connection, and whatever the HTTP reader had
// already buffered from it. Reading the connection directly would drop those bytes.
type debugLeg struct {
	conn net.Conn
	r    io.Reader
}

// debugWait is one parent leg waiting for its phone.
type debugWait struct {
	device uuid.UUID
	legs   chan debugLeg // unbuffered: a hand-off either lands or the phone's leg is closed
	failed chan string   // buffered 1: the phone's own reason, from its acknowledgement
	done   chan struct{} // closed when the parent leg stops waiting, for any reason
}

// debugRelay is the registry of parent legs waiting for their phone. In memory, because a stream
// is a pair of live connections to this process: a waiting entry in a database would outlive the
// connection it describes. It assumes one replica, which is what the deployment runs.
type debugRelay struct {
	mu      sync.Mutex
	waiting map[string]*debugWait
}

func newDebugRelay() *debugRelay {
	return &debugRelay{waiting: map[string]*debugWait{}}
}

// open registers a wait and returns its stream id and the function that withdraws it.
//
// The id is 128 random bits and is the only thing the phone presents besides its own token. It is
// not the security boundary — the device token is, and claim checks that the device dialling is the
// device the stream was opened for — but it keeps two streams to one phone from being confused.
func (r *debugRelay) open(device uuid.UUID) (string, *debugWait, func()) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand does not fail on any platform this runs on; if it ever does, a predictable
		// id is the wrong answer and a crash is the right one.
		panic("debug relay: no randomness: " + err.Error())
	}
	id := hex.EncodeToString(b[:])
	w := &debugWait{
		device: device,
		legs:   make(chan debugLeg),
		failed: make(chan string, 1),
		done:   make(chan struct{}),
	}
	r.mu.Lock()
	r.waiting[id] = w
	r.mu.Unlock()
	var once sync.Once
	return id, w, func() {
		once.Do(func() {
			r.mu.Lock()
			delete(r.waiting, id)
			r.mu.Unlock()
			close(w.done)
		})
	}
}

// claim hands the wait for stream to the device dialling it, exactly once. Removing it here is what
// makes a second dial of the same id a 404 rather than a second splice.
func (r *debugRelay) claim(stream string, device uuid.UUID) (*debugWait, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	w, ok := r.waiting[stream]
	if !ok || w.device != device {
		return nil, false
	}
	delete(r.waiting, stream)
	return w, true
}

// fail delivers the phone's reason for not dialling, so the parent reads the sentence the phone
// wrote rather than waiting out the window and reading a timeout.
func (r *debugRelay) fail(stream string, device uuid.UUID, reason string) {
	r.mu.Lock()
	w, ok := r.waiting[stream]
	r.mu.Unlock()
	if !ok || w.device != device {
		return
	}
	select {
	case w.failed <- reason:
	default:
	}
}

// upgradeRequested reports whether r asks for the debug protocol. Both headers, because a proxy
// that forwarded Upgrade and dropped Connection would hand this handler a request it cannot
// answer with 101.
func upgradeRequested(r *http.Request) bool {
	return headerHasToken(r.Header, "Connection", "upgrade") &&
		strings.EqualFold(strings.TrimSpace(r.Header.Get("Upgrade")), debugUpgrade)
}

func headerHasToken(h http.Header, name, token string) bool {
	for _, v := range h.Values(name) {
		for _, part := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(part), token) {
				return true
			}
		}
	}
	return false
}

func refuseWithoutUpgrade(c *gin.Context) {
	c.Header("Upgrade", debugUpgrade)
	c.Header("Connection", "Upgrade")
	failWith(c, http.StatusUpgradeRequired, "upgrade_required",
		"this endpoint carries a raw adb stream; open it with `fgctl adb`, which asks for the upgrade")
}

// switchProtocols takes the connection from net/http and answers 101 on it.
//
// The deadlines are cleared explicitly, and that line is belt and braces rather than the fix it
// looks like. The server's ReadTimeout IS set on the connection itself, but net/http resets the
// read deadline to zero on the way out of Hijack (connReader.abortPendingRead) — measured: with
// this line removed, a stream idle for 35 s still carried bytes. It stays because the day somebody
// gives the server a WriteTimeout, that deadline would survive the hijack and end every adb
// session at the same second. TestRemoteADBOutlivesTheRequestTimeout is what notices either.
func switchProtocols(c *gin.Context) (debugLeg, error) {
	conn, brw, err := c.Writer.Hijack()
	if err != nil {
		return debugLeg{}, err
	}
	_ = conn.SetDeadline(time.Time{})
	if _, err := brw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: " + debugUpgrade +
		"\r\nConnection: Upgrade\r\n\r\n"); err != nil {
		conn.Close()
		return debugLeg{}, err
	}
	if err := brw.Flush(); err != nil {
		conn.Close()
		return debugLeg{}, err
	}
	var r io.Reader = conn
	if brw.Reader.Buffered() > 0 {
		r = io.MultiReader(io.LimitReader(brw.Reader, int64(brw.Reader.Buffered())), conn)
	}
	return debugLeg{conn: conn, r: r}, nil
}

// splice copies both ways until either side ends, the session ceiling passes, or ctx ends, and
// then closes both. Closing both on the first end is right for adb, which never half-closes, and
// it is the only way to unblock the copy running the other direction.
func splice(ctx context.Context, a, b debugLeg, ceiling time.Duration) (aToB, bToA int64) {
	var once sync.Once
	closeBoth := func() {
		once.Do(func() {
			a.conn.Close()
			b.conn.Close()
		})
	}
	timer := time.AfterFunc(ceiling, closeBoth)
	defer timer.Stop()
	stop := context.AfterFunc(ctx, closeBoth)
	defer stop()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		aToB, _ = io.Copy(b.conn, a.r)
		closeBoth()
	}()
	go func() {
		defer wg.Done()
		bToA, _ = io.Copy(a.conn, b.r)
		closeBoth()
	}()
	wg.Wait()
	return aToB, bToA
}

// openDebugStream is the parent's leg (FR-19.1).
//
// Everything that can be refused is refused BEFORE the command is queued and before the upgrade, as
// an ordinary JSON error: a switch that is off, a phone that never enrolled, a malformed port. Once
// the phone has been asked, the only remaining answers are the phone's own reason, silence, or a
// stream.
func (s *Server) openDebugStream(c *gin.Context) {
	id, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	if !upgradeRequested(c.Request) {
		refuseWithoutUpgrade(c)
		return
	}

	target := c.DefaultQuery("target", "connect")
	if target != "connect" && target != "pair" {
		failWith(c, http.StatusBadRequest, "invalid_input",
			`target must be "connect" (an adb connection) or "pair" (the one-time pairing)`)
		return
	}
	port := 0
	if raw := c.Query("port"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 65535 {
			failWith(c, http.StatusBadRequest, "invalid_input",
				"port must be a number from 1 to 65535, or absent to let the phone find its own")
			return
		}
		port = n
	}

	ctx := c.Request.Context()
	dev, err := s.store.GetDevice(ctx, id)
	if err != nil {
		s.fail(c, err)
		return
	}
	if dev.EnrolledAt == nil {
		failWith(c, http.StatusConflict, "conflict", "this device has not enrolled yet")
		return
	}
	pol, err := s.store.GetPolicy(ctx, dev.ChildID)
	if err != nil {
		s.fail(c, err)
		return
	}
	if !pol.AllowDebugging {
		failWith(c, http.StatusConflict, "debugging_off",
			"Allow debugging is off for this child, so adb is not running on the phone. Switch it on "+
				"under Rules and wait for the phone to apply it, then try again.")
		return
	}

	stream, wait, withdraw := s.debug.open(id)
	defer withdraw()

	cmd, err := s.store.QueueCommand(ctx, id, store.CmdTypeOpenDebugStream,
		map[string]any{"stream": stream, "target": target, "port": port}, parentID(c), debugCommandTTL)
	if err != nil {
		s.fail(c, err)
		return
	}
	// FR-19.6: audited when asked for, and again below when it ends.
	s.auditParent(c, "DEBUG_STREAM_REQUESTED", "device", id.String(), map[string]any{
		"command": cmd.ID.String(), "target": target, "port": port,
	})
	s.hub.PublishDevice(id, Event{Type: "command", ChildID: dev.ChildID.String()})
	s.hub.PublishParents(Event{Type: "command", DeviceID: id.String(), ChildID: dev.ChildID.String()})

	timer := time.NewTimer(debugDialWindow)
	defer timer.Stop()
	var phone debugLeg
	select {
	case phone = <-wait.legs:
	case reason := <-wait.failed:
		failWith(c, http.StatusBadGateway, "phone_refused",
			"the phone could not open the debug connection: "+reason)
		return
	case <-timer.C:
		failWith(c, http.StatusGatewayTimeout, "phone_silent",
			"the phone did not dial back within "+debugDialWindow.String()+". It is offline, or it runs a "+
				"FamilyGuard that does not know OPEN_DEBUG_STREAM (older than 0.6.13).")
		return
	case <-ctx.Done():
		return
	}

	parent, err := switchProtocols(c)
	if err != nil {
		phone.conn.Close()
		s.log.Warn("debug stream: the parent leg could not be taken over", "error", err,
			"request_id", RequestIDOf(c))
		return
	}
	// The request's context is cancelled when the client goes away; after the hijack that is the
	// splice's business, and the audit below must still be written when it happens.
	c.Request = c.Request.WithContext(context.WithoutCancel(ctx))

	started := s.now()
	up, down := splice(context.Background(), parent, phone, debugMaxSession)
	s.auditParent(c, "DEBUG_STREAM_CLOSED", "device", id.String(), map[string]any{
		"command": cmd.ID.String(), "target": target,
		"seconds": int(s.now().Sub(started).Seconds()), "bytes_to_phone": up, "bytes_from_phone": down,
	})
}

// deviceDebugStream is the phone's leg (FR-19.2). The device token authenticates it; the stream id
// only says which waiting parent it belongs to, and claim refuses an id opened for another phone.
func (s *Server) deviceDebugStream(c *gin.Context) {
	dev := deviceOf(c)
	if !upgradeRequested(c.Request) {
		refuseWithoutUpgrade(c)
		return
	}
	wait, ok := s.debug.claim(c.Param("stream"), dev.ID)
	if !ok {
		failWith(c, http.StatusNotFound, "not_found",
			"no debug stream is waiting under that id for this device; the parent may have given up")
		return
	}
	leg, err := switchProtocols(c)
	if err != nil {
		s.log.Warn("debug stream: the phone leg could not be taken over", "error", err,
			"device", dev.ID.String())
		return
	}
	select {
	case wait.legs <- leg:
	case <-wait.done:
		// The parent stopped waiting between the claim and now. The phone sees its stream close
		// straight after the 101 and reports that, which is what happened.
		leg.conn.Close()
	}
}

// debugStreamOf is the stream id an OPEN_DEBUG_STREAM command was queued with, or "".
func debugStreamOf(cmd *store.Command) string {
	if cmd == nil || cmd.Type != store.CmdTypeOpenDebugStream {
		return ""
	}
	v, _ := cmd.Params["stream"].(string)
	return v
}
