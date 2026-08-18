package io.github.helios57.familyguard.net

import kotlinx.coroutines.channels.Channel
import kotlinx.coroutines.coroutineScope
import kotlinx.coroutines.launch
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.withTimeout
import kotlinx.coroutines.yield
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test

/**
 * [EventStream] against a server that really closes connections.
 *
 * Real time, not a test scheduler: every claim here is about what happens when a socket dies, and a
 * virtual clock cannot close one. The backoff is configured down to a few milliseconds so the class
 * runs in well under a second — the schedule itself is measured in [BackoffTest], where it can be
 * measured exactly rather than waited out.
 */
class EventStreamTest {

    private lateinit var server: LoopbackServer
    private var body: String = ""
    private var status: Int = 200

    @Before
    fun start() {
        server = LoopbackServer {
            if (status == 200) {
                HttpResponse(200, contentType = "text/event-stream", body = body, stream = true)
            } else {
                HttpResponse(status, body = """{"error":"$body","message":"no","request_id":"r"}""")
            }
        }
    }

    @After
    fun stop() {
        server.close()
    }

    /** Short enough that the suite stays fast, long enough to still be a delay. */
    private fun fastBackoff() = Backoff(baseMillis = 2, maxMillis = 4)

    private fun stream(backoff: Backoff, onWake: suspend (SseEvent) -> Unit) =
        EventStream(ApiClient(server.baseUrl, token = { "device-token" }), backoff = backoff, onWake = onWake)

    @Test
    fun `an event on the stream wakes the caller`() = runBlocking {
        body = "event: connected\ndata: {}\n\nevent: policy_changed\ndata: {\"v\":7}\n\n"
        val woken = Channel<SseEvent>(Channel.UNLIMITED)
        val stream = stream(fastBackoff()) { woken.send(it) }

        val job = launch { stream.run() }
        val event = withTimeout(TIMEOUT) { woken.receive() }
        job.cancel()

        assertEquals("policy_changed", event.type)
        assertEquals("{\"v\":7}", event.data)
    }

    @Test
    fun `the connected frame is not a wake-up`() = runBlocking {
        // It marks the stream as established, which is the backoff's business and nobody else's. A
        // sync per connection would mean a phone on a flapping network syncing continuously.
        body = "event: connected\ndata: {}\n\nevent: command\ndata: c1\n\n"
        val woken = Channel<SseEvent>(Channel.UNLIMITED)
        val stream = stream(fastBackoff()) { woken.send(it) }

        val job = launch { stream.run() }
        val first = withTimeout(TIMEOUT) { woken.receive() }
        job.cancel()

        assertEquals("command", first.type)
    }

    @Test
    fun `a keepalive is not a wake-up`() = runBlocking {
        // The server writes one every twenty seconds for as long as the phone is connected. A stream
        // that woke a sync on each of them would poll three times a minute, all day, on battery,
        // while reporting that nothing had changed.
        body = ":\n:ping\n\nevent: command\ndata: c1\n\n"
        val woken = Channel<SseEvent>(Channel.UNLIMITED)
        val stream = stream(fastBackoff()) { woken.send(it) }

        val job = launch { stream.run() }
        val first = withTimeout(TIMEOUT) { woken.receive() }
        job.cancel()

        assertEquals("command", first.type)
        assertEquals("c1", first.data)
    }

    @Test
    fun `a closed stream is reconnected`() = runBlocking {
        // The server closes every connection at fifteen minutes by design, and a phone changing
        // networks closes them far more often. A stream that ended when the socket did would be a
        // phone that stops receiving commands after its first tunnel.
        body = "event: connected\ndata: {}\n\nevent: command\ndata: c\n\n"
        val woken = Channel<SseEvent>(Channel.UNLIMITED)
        val stream = stream(fastBackoff()) { woken.send(it) }

        val job = launch { stream.run() }
        withTimeout(TIMEOUT) { repeat(3) { woken.receive() } }
        job.cancel()

        assertTrue("${server.requests.size}", server.requests.size >= 3)
    }

    @Test
    fun `the backoff resets on the connected frame and not on a bare connection`() = runBlocking {
        // Two runs of the same shape, differing only in whether the server says `connected`. The
        // failure count seen at wake-up time is the observable: zero means the stream was recognised
        // as established, and growing means every reconnect is still being counted as a failure.
        body = "event: connected\ndata: {}\n\nevent: command\ndata: c\n\n"
        assertEquals(listOf(0, 0, 0), failuresSeenAtWake(fastBackoff()))

        // A server that accepts the socket and never establishes the stream — a proxy in front of a
        // restarting backend does exactly this — must not hold the device at the base delay forever.
        body = "event: command\ndata: c\n\n"
        assertEquals(listOf(0, 1, 2), failuresSeenAtWake(fastBackoff()))
    }

    private suspend fun failuresSeenAtWake(backoff: Backoff): List<Int> = coroutineScope {
        val seen = Channel<Int>(Channel.UNLIMITED)
        val stream = stream(backoff) { seen.send(backoff.failures) }
        val job = launch { stream.run() }
        val counts = withTimeout(TIMEOUT) { List(3) { seen.receive() } }
        job.cancel()
        counts
    }

    @Test
    fun `a revoked credential ends the loop instead of retrying forever`() = runBlocking {
        // 401 means this device's token is no longer accepted, and no amount of waiting changes
        // that. A phone that retried it every few seconds for the rest of its life would be a
        // battery complaint with no other symptom, and a log full of failed authentications that
        // reads exactly like a stolen token being probed.
        status = 401
        body = "unauthorized"
        val stream = stream(fastBackoff()) { throw AssertionError("must not wake on a refusal") }

        withTimeout(TIMEOUT) { stream.run() }

        assertEquals(1, server.requests.size)
        assertEquals(401, stream.lastFatal?.status)
        assertEquals("unauthorized", stream.lastFatal?.code)
    }

    @Test
    fun `a server fault is retried`() = runBlocking {
        status = 503
        body = "unavailable"
        val stream = stream(fastBackoff()) { throw AssertionError("must not wake on a refusal") }

        val job = launch { stream.run() }
        withTimeout(TIMEOUT) { while (server.requests.size < 3) yield() }
        job.cancel()

        assertNull(stream.lastFatal)
    }

    private companion object {
        /** Generous: it exists to fail the build rather than hang it, not to measure anything. */
        const val TIMEOUT = 15_000L
    }
}
