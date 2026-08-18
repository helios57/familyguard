package io.github.helios57.familyguard.net

import java.io.BufferedReader
import java.io.InputStreamReader
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.Before
import org.junit.Test

/**
 * [ApiClient] against a real HTTP server on loopback — see [LoopbackServer] for why it is a socket
 * rather than a mock.
 */
class ApiClientTest {

    private lateinit var server: LoopbackServer

    private var status: Int = 200
    private var responseBody: String = "{}"
    private var contentType: String = "application/json"

    @Before
    fun start() {
        server = LoopbackServer { HttpResponse(status, contentType = contentType, body = responseBody) }
    }

    @After
    fun stop() {
        server.close()
    }

    private fun client(token: String? = "device-token", base: String = server.baseUrl) =
        ApiClient(base, token = { token })

    // ---- what goes out ---------------------------------------------------------------------

    @Test
    fun `enroll posts the token and does not authenticate`() {
        responseBody = """
            {"device_token":"dt","device_id":"dev-1","child_id":"child-1",
             "recovery":{"salt":"c2FsdA==","iterations":600000,"hash":"aGFzaA=="}}
        """.trimIndent()

        val response = client(token = null).enroll(EnrollRequest(enrollmentToken = "et-1", model = "Pixel 4a"))

        val request = server.last!!
        assertEquals("POST", request.method)
        assertEquals("/api/v1/enroll", request.path)
        // Enrollment is the one call a device makes before it has a credential. Sending a bearer
        // header here would mean the client had one to send, which is the bug this asserts against.
        assertNull(request.header("Authorization"))
        assertTrue(request.body, request.body.contains("\"enrollment_token\":\"et-1\""))
        assertTrue(request.body, request.body.contains("\"model\":\"Pixel 4a\""))
        assertEquals("dt", response.deviceToken)
        assertEquals("dev-1", response.deviceId)
        assertEquals(600_000, response.recovery.iterations)
    }

    @Test
    fun `authenticated calls carry the bearer token`() {
        responseBody = """{"desired":{"locked":true,"policy_version":7},"input":{"used_minutes_today":42}}"""

        val response = client().policy()

        val request = server.last!!
        assertEquals("GET", request.method)
        assertEquals("/api/v1/device/policy", request.path)
        assertEquals("Bearer device-token", request.header("Authorization"))
        assertTrue(response.desired.locked)
        assertEquals(7L, response.desired.policyVersion)
        assertEquals(42, response.input.usedMinutesToday)
    }

    @Test
    fun `the token is read per request, not captured at construction`() {
        // Enrollment replaces the credential while this client instance is alive. A client that
        // captured the token would go on sending the one it was built with — which, on a device that
        // has just enrolled, is null.
        var token: String? = "first"
        val api = ApiClient(server.baseUrl, token = { token })
        api.policy()
        assertEquals("Bearer first", server.last!!.header("Authorization"))

        token = "second"
        api.policy()
        assertEquals("Bearer second", server.last!!.header("Authorization"))
    }

    @Test
    fun `a trailing slash on the base URL does not double the path separator`() {
        client(base = "${server.baseUrl}/").policy()

        assertEquals("/api/v1/device/policy", server.last!!.path)
    }

    @Test
    fun `an authenticated call without a token fails before it reaches the network`() {
        try {
            client(token = null).policy()
            fail("expected an IllegalStateException")
        } catch (e: IllegalStateException) {
            assertTrue(e.message!!, e.message!!.contains("/api/v1/device/policy"))
        }
        // Nothing was sent: an unauthenticated poll would be recorded by the server as a device
        // check-in that failed authentication, which is what a stolen token being probed looks like.
        assertTrue(server.requests.isEmpty())
    }

    @Test
    fun `heartbeat omits fields the device could not measure`() {
        // Absent and zero are different facts. `battery_level: 0` is a phone about to die; a phone
        // that could not read its battery must not be reported as one.
        responseBody = """{"policy_version":3,"pending_commands":2}"""

        val response = client().heartbeat(HeartbeatRequest(connectivity = "wifi", policyVersion = 3))

        val request = server.last!!
        assertEquals("POST", request.method)
        assertEquals("/api/v1/device/heartbeat", request.path)
        assertFalse(request.body, request.body.contains("battery_level"))
        assertTrue(request.body, request.body.contains("\"connectivity\":\"wifi\""))
        assertEquals(3L, response.policyVersion)
        assertEquals(2, response.pendingCommands)
    }

    @Test
    fun `a field the server adds later does not break a deployed phone`() {
        // The phones outlive the backend's schema. An unknown key must be ignored rather than throw,
        // or the first additive server change bricks every device that has not been updated.
        responseBody = """{"desired":{"locked":true},"input":{},"issued_at":"2026-08-17T10:00:00Z"}"""

        assertTrue(client().policy().desired.locked)
    }

    // ---- what comes back -------------------------------------------------------------------

    @Test
    fun `a refusal carries the server's own explanation`() {
        status = 409
        responseBody = """{"error":"conflict","message":"enrollment token already used","request_id":"req-9"}"""

        try {
            client(token = null).enroll(EnrollRequest(enrollmentToken = "spent"))
            fail("expected an ApiException")
        } catch (e: ApiException) {
            assertEquals(409, e.status)
            assertEquals("conflict", e.code)
            assertEquals("enrollment token already used", e.detail)
            assertEquals("req-9", e.requestId)
            // A spent single-use token is spent for good. Retrying it forever is how a phone that
            // failed to enroll once never enrolls at all.
            assertFalse(e.retryable)
        }
    }

    @Test
    fun `a revoked credential is not retryable and a server fault is`() {
        status = 401
        responseBody = """{"error":"unauthorized","message":"device token revoked","request_id":"req-1"}"""
        val revoked = refusalFrom { client().policy() }
        assertEquals(401, revoked.status)
        assertFalse(revoked.retryable)

        status = 503
        responseBody = """{"error":"unavailable","message":"restarting","request_id":"req-2"}"""
        val restarting = refusalFrom { client().policy() }
        assertEquals(503, restarting.status)
        assertTrue(restarting.retryable)

        status = 429
        responseBody = """{"error":"rate_limited","message":"slow down","request_id":"req-3"}"""
        assertTrue(refusalFrom { client().policy() }.retryable)
    }

    @Test
    fun `an error body that is not the envelope is truncated, not parsed`() {
        // A proxy in front of the backend answers with its own HTML. Carrying that into a log line
        // as a "message" turns one failed poll into a screenful.
        status = 502
        contentType = "text/html"
        responseBody = "<html><body>" + "x".repeat(4_000) + "</body></html>"

        val e = refusalFrom { client().policy() }

        assertEquals(502, e.status)
        assertEquals("http_502", e.code)
        assertEquals(200, e.detail.length)
        assertEquals("", e.requestId)
    }

    @Test
    fun `an empty error body still produces a usable refusal`() {
        status = 500
        responseBody = ""

        val e = refusalFrom { client().policy() }

        assertEquals(500, e.status)
        assertEquals("http_500", e.code)
        assertEquals("", e.detail)
        assertTrue(e.retryable)
    }

    // ---- the stream ------------------------------------------------------------------------

    @Test
    fun `openStream asks for the event stream and hands back a readable connection`() {
        server.answerWith {
            HttpResponse(
                200,
                contentType = "text/event-stream",
                body = "event: connected\ndata: {}\n\n",
                stream = true,
            )
        }

        val connection = client().openStream()
        try {
            val request = server.last!!
            assertEquals("/api/v1/device/stream", request.path)
            assertEquals("Bearer device-token", request.header("Authorization"))
            assertEquals("text/event-stream", request.header("Accept"))
            // Well above the server's 20s keepalive: a deadline shorter than the keepalive turns a
            // healthy idle stream into a reconnect loop, which looks exactly like a flaky network.
            assertTrue(ApiClient.STREAM_READ_TIMEOUT_MILLIS > 20_000)
            assertEquals(ApiClient.STREAM_READ_TIMEOUT_MILLIS, connection.readTimeout)

            val reader = BufferedReader(InputStreamReader(connection.inputStream, Charsets.UTF_8))
            assertEquals("event: connected", reader.readLine())
        } finally {
            connection.disconnect()
        }
    }

    @Test
    fun `openStream throws rather than returning a connection the server refused`() {
        status = 403
        responseBody = """{"error":"forbidden","message":"device disabled","request_id":"req-4"}"""

        val e = refusalFrom { client().openStream() }

        // Returning the connection and letting the caller discover the status is how a reconnect
        // loop ends up parsing an error page as an event stream, forever, at full speed.
        assertEquals(403, e.status)
        assertEquals("device disabled", e.detail)
        assertFalse(e.retryable)
    }

    private fun refusalFrom(call: () -> Unit): ApiException {
        try {
            call()
        } catch (e: ApiException) {
            return e
        }
        fail("expected an ApiException")
        error("unreachable")
    }
}
