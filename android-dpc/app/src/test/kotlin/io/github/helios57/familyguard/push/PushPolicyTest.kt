package io.github.helios57.familyguard.push

import kotlinx.serialization.json.Json
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * FR-26.3: a resting phone relies on a push only once one has actually arrived for the token it holds.
 * Until then — no token, a new token, a server without push — it polls every five minutes as before.
 */
class PushPolicyTest {

    private val lenient = Json { ignoreUnknownKeys = true }
    private val options = PushOptions(projectId = "p", applicationId = "1:2:android:3", apiKey = "k", senderId = "2")

    @Test
    fun `no token polls every five minutes`() =
        assertEquals(5 * 60_000L, PushPolicy.pollMillis(token = "", verifiedToken = ""))

    @Test
    fun `a token nothing has arrived for yet still polls every five minutes`() =
        assertEquals(5 * 60_000L, PushPolicy.pollMillis(token = "t1", verifiedToken = ""))

    @Test
    fun `a token a push arrived for polls every thirty minutes`() =
        assertEquals(30 * 60_000L, PushPolicy.pollMillis(token = "t1", verifiedToken = "t1"))

    @Test
    fun `a push that arrived for the previous token proves nothing about the new one`() =
        assertEquals(5 * 60_000L, PushPolicy.pollMillis(token = "t2", verifiedToken = "t1"))

    @Test
    fun `options with a blank field are not usable`() {
        assertTrue(options.usable)
        assertFalse(options.copy(apiKey = "").usable)
        assertFalse(options.copy(projectId = " ").usable)
    }

    @Test
    fun `the plan starts, keeps, restarts and stops`() {
        assertEquals(PushPolicy.Plan.START, PushPolicy.plan(running = null, wanted = options))
        assertEquals(PushPolicy.Plan.KEEP, PushPolicy.plan(running = options, wanted = options.copy()))
        assertEquals(PushPolicy.Plan.RESTART, PushPolicy.plan(running = options, wanted = options.copy(projectId = "q")))
        assertEquals(PushPolicy.Plan.STOP, PushPolicy.plan(running = options, wanted = null))
        assertEquals(PushPolicy.Plan.STOP, PushPolicy.plan(running = options, wanted = options.copy(senderId = "")))
        assertEquals(PushPolicy.Plan.KEEP, PushPolicy.plan(running = null, wanted = null))
    }

    @Test
    fun `the heartbeat carries the token only while the server sends push`() {
        assertEquals("t1", PushPolicy.reportedToken(options, "t1"))
        assertNull("no token yet leaves what the server has", PushPolicy.reportedToken(options, ""))
        assertNull("a server without push is told nothing", PushPolicy.reportedToken(null, "t1"))
    }

    @Test
    fun `the server's options parse from the policy`() {
        val parsed = lenient.decodeFromString(
            PushOptions.serializer(),
            """{"project_id":"p","application_id":"1:2:android:3","api_key":"k","sender_id":"2","extra":1}""",
        )
        assertEquals(options, parsed)
    }
}
