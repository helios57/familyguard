package io.github.helios57.familyguard.net

import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.IOException
import java.net.HttpURLConnection
import java.net.InetSocketAddress
import java.net.Socket
import java.net.URL

/**
 * The fixture's own tests.
 *
 * [LoopbackServer.stopAnswering] is not a convenience — it is how nine tests across two classes
 * spell *the server cannot be reached*, and it replaced `close()`, which spelled the same thing as a
 * claim about a port the fixture had just released. That made those nine tests depend on nothing in
 * the JVM binding an ephemeral port in a window they did not control, and twice — once on CI — one
 * of them lost: a recovered device came back under management because something answered a request
 * the test had arranged for nobody to answer.
 *
 * So the replacement is held to all three of the properties that make it a fixture rather than a
 * hope. Any one of them alone passes on a broken implementation: a `close()` also produces the
 * `IOException`, and a server that simply stopped accepting would also hold the port.
 */
class LoopbackServerTest {

    private val server = LoopbackServer { HttpResponse(200, body = """{"ok":true}""") }

    @After
    fun tearDown() = server.close()

    private fun get(): HttpURLConnection =
        (URL(server.baseUrl + "/api/v1/device/policy").openConnection() as HttpURLConnection).apply {
            connectTimeout = 5_000
            readTimeout = 5_000
        }

    /** The half that proves the calibration is not measuring a server that was never up. */
    @Test
    fun `a running server answers`() {
        val connection = get()
        try {
            assertEquals(200, connection.responseCode)
            assertEquals("""{"ok":true}""", connection.inputStream.readBytes().toString(Charsets.UTF_8))
        } finally {
            connection.disconnect()
        }
    }

    /**
     * One: the port stays bound, which is the entire reason this exists.
     *
     * Asserted with a bare TCP connect rather than through HTTP, because HTTP cannot tell "the
     * connection was refused" from "the connection was accepted and dropped" — both surface as an
     * `IOException`, and that indistinguishability is what let the original defect look like a
     * working test for as long as it did.
     */
    @Test
    fun `a silent server still owns its port`() {
        server.stopAnswering()

        val address = InetSocketAddress(
            server.baseUrl.removePrefix("http://").substringBefore(':'),
            server.baseUrl.substringAfterLast(':').toInt(),
        )
        Socket().use { it.connect(address, 5_000) }
    }

    /** Two: the client sees a failure, and specifically the one a refused connection produces. */
    @Test
    fun `a silent server fails the request with an IOException`() {
        server.stopAnswering()

        val connection = get()
        try {
            val status = connection.responseCode
            throw AssertionError("expected the request to fail, got HTTP $status")
        } catch (e: IOException) {
            assertTrue("not the end-of-stream failure: $e", e.message.orEmpty().isNotEmpty())
        } finally {
            connection.disconnect()
        }
    }

    /**
     * Three: the request is read before the connection is dropped.
     *
     * This is what keeps `stopAnswering` a statement about the *server* rather than about the wire.
     * Dropping the connection on accept would reset it while the client was still writing, and the
     * client's own request would fail — a test asserting the device tried would then be asserting
     * something that had not been established.
     */
    @Test
    fun `a silent server still records what it was asked`() {
        server.stopAnswering()

        val connection = get()
        try {
            connection.responseCode
        } catch (_: IOException) {
            // expected
        } finally {
            connection.disconnect()
        }

        // Not "exactly one": `HttpURLConnection` retries an idempotent request once when the peer
        // closes before the status line, so a silent server sees the GET twice. Measured, not
        // assumed — and deliberately not pinned, because the count is the JDK's business and a test
        // that asserted 2 would go red on a JDK that stopped retrying without anything being wrong.
        assertTrue("the server recorded nothing", server.requests.isNotEmpty())
        assertTrue(
            "recorded something other than the request under test: ${server.requests.map { it.path }}",
            server.requests.all { it.path == "/api/v1/device/policy" && it.method == "GET" },
        )
        assertNotNull(server.last!!.header("Accept"))
    }

    /**
     * `close()` keeps its old meaning; teardown depends on it.
     *
     * What this must **not** assert is that the client's request then fails. That was its first
     * form — `expected the request to fail, got HTTP $status` — and it is the same defect the rest of
     * this file exists to remove, left standing in the one test whose subject is `close` itself.
     * `close()` releases the port, so "the request fails" is a claim about a port the fixture no
     * longer owns, and it lost the bet: measured red on 2026-08-18 with the other 446 tests green,
     * which is how a false red gets manufactured and then blamed on whatever was being changed at
     * the time. The mechanism was then reproduced deliberately rather than inferred — close an
     * ephemeral port, bind it again from the same JVM, answer 200, and the old assertion fails with
     * that exact message.
     *
     * So both halves asserted here are owned:
     *
     *  1. the socket is shut, which is what distinguishes `close` from [LoopbackServer.stopAnswering]
     *     and what returns the port and ends the accept thread;
     *  2. whatever may now be listening on that address, this server did not serve it.
     *
     * The second is a second reading of the first rather than independent evidence — it can only go
     * red if `close()` failed to close — and it is here because it is exactly the distinction the old
     * assertion could not make.
     */
    @Test
    fun `a closed server stops serving and gives up its port`() {
        server.close()

        assertTrue("close() left the listening socket open", server.isClosed)

        val connection = get()
        try {
            connection.responseCode
        } catch (_: IOException) {
            // Expected while the port stays free — but deliberately not asserted, because by then it
            // is no longer this fixture's port to make claims about.
        } finally {
            connection.disconnect()
        }

        assertTrue(
            "a closed server served ${server.requests.size} request(s): ${server.requests.map { it.path }}",
            server.requests.isEmpty(),
        )
    }
}
