package io.github.helios57.familyguard.enroll

import io.github.helios57.familyguard.net.ApiClient
import io.github.helios57.familyguard.net.HttpResponse
import io.github.helios57.familyguard.net.LoopbackServer
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test

/** A [CredentialStore] that keeps the record in a field. The real one needs a keystore. */
class InMemoryCredentialStore(private var stored: Credentials? = null) : CredentialStore {
    var writes: Int = 0
        private set

    override fun load(): Credentials? = stored

    override fun save(credentials: Credentials) {
        stored = credentials
        writes++
    }

    override fun clear() {
        stored = null
    }
}

class EnrollerTest {

    private lateinit var server: LoopbackServer
    private var status = 200
    private var body = SUCCESS

    @Before
    fun start() {
        server = LoopbackServer { HttpResponse(status, body = body) }
    }

    @After
    fun stop() {
        server.close()
    }

    private fun enroller(
        store: CredentialStore,
        cleartextAllowed: Boolean = true,
    ) = Enroller(store, cleartextAllowed = cleartextAllowed) { ApiClient(it, token = { null }) }

    private fun extras(
        serverUrl: String? = null,
        token: String? = "et-1",
    ): Map<String, String?> = mapOf(
        Enroller.EXTRA_SERVER_URL to (serverUrl ?: server.baseUrl),
        Enroller.EXTRA_ENROLLMENT_TOKEN to token,
        Enroller.EXTRA_DEVICE_ID to "dev-1",
    )

    @Test
    fun `a successful enrollment is stored whole`() {
        val store = InMemoryCredentialStore()

        val result = enroller(store).enroll(
            extras(),
            DeviceFacts(model = "Pixel 4a", osVersion = "14", criticalPackages = listOf("com.oem.dialer")),
        )

        val enrolled = result as EnrollResult.Enrolled
        assertEquals("dt", enrolled.credentials.deviceToken)
        assertEquals("dev-1", enrolled.credentials.deviceId)
        assertEquals("child-1", enrolled.credentials.childId)
        assertEquals(server.baseUrl, enrolled.credentials.serverUrl)
        assertEquals(600_000, enrolled.credentials.recovery.iterations)
        assertEquals(enrolled.credentials, store.load())

        val request = server.last!!
        assertTrue(request.body, request.body.contains("\"enrollment_token\":\"et-1\""))
        assertTrue(request.body, request.body.contains("com.oem.dialer"))
    }

    @Test
    fun `a device that already has a credential does not spend a second token`() {
        // `onProfileProvisioningComplete` is not an exactly-once delivery, and the setup activity can
        // be re-entered. The enrollment token is single-use: a second attempt would answer 409 and
        // could leave a perfectly working device reporting that it had failed to enroll.
        val existing = Credentials("https://guard.example", "dt-old", "dev-old", "child-old")
        val store = InMemoryCredentialStore(existing)

        val result = enroller(store).enroll(extras())

        assertEquals(existing, (result as EnrollResult.AlreadyEnrolled).credentials)
        assertEquals(0, store.writes)
        assertTrue(server.requests.isEmpty())
    }

    @Test
    fun `a spent token is refused permanently, not retried`() {
        status = 409
        body = """{"error":"conflict","message":"already used","request_id":"r"}"""
        val store = InMemoryCredentialStore()

        val result = enroller(store).enroll(extras())

        assertEquals(409, (result as EnrollResult.Refused).cause.status)
        assertNull(store.load())
    }

    @Test
    fun `a backend that is restarting is deferred, not refused`() {
        // The distinction is the whole point of the type: a phone provisioned while the backend is
        // rolling out must end up enrolled, and a phone whose token is spent must stop trying.
        status = 503
        body = """{"error":"unavailable","message":"restarting","request_id":"r"}"""

        val result = enroller(InMemoryCredentialStore()).enroll(extras())

        assertTrue(result.toString(), result is EnrollResult.Deferred)
    }

    @Test
    fun `an unreachable server is deferred`() {
        // The ordinary first attempt on a freshly provisioned phone in a home: Wi-Fi associated, the
        // request goes out, nothing comes back. `stopAnswering` rather than `close` because a closed
        // port is not this fixture's to speak for — see `LoopbackServer.stopAnswering`.
        server.stopAnswering()

        val result = enroller(InMemoryCredentialStore()).enroll(extras(serverUrl = server.baseUrl))

        assertTrue(result.toString(), result is EnrollResult.Deferred)
    }

    @Test
    fun `a 200 with no usable credential is deferred rather than stored`() {
        // Storing this is the one unrecoverable outcome: the device believes it is enrolled, cannot
        // authenticate, and the idempotence guard above will refuse to try again — while the
        // enrollment token has already been spent server-side.
        body = """{"device_token":"","device_id":"","child_id":"child-1"}"""
        val store = InMemoryCredentialStore()

        val result = enroller(store).enroll(extras())

        assertTrue(result.toString(), result is EnrollResult.Deferred)
        assertNull(store.load())
        assertEquals(0, store.writes)
    }

    // ---- what the QR code got wrong --------------------------------------------------------

    @Test
    fun `missing extras are misprovisioning, not a network failure`() {
        val store = InMemoryCredentialStore()

        val noUrl = enroller(store).enroll(mapOf(Enroller.EXTRA_ENROLLMENT_TOKEN to "et-1"))
        assertTrue(
            (noUrl as EnrollResult.Misprovisioned).reason,
            noUrl.reason.contains(Enroller.EXTRA_SERVER_URL),
        )

        val noToken = enroller(store).enroll(extras(token = null))
        assertTrue(
            (noToken as EnrollResult.Misprovisioned).reason,
            noToken.reason.contains(Enroller.EXTRA_ENROLLMENT_TOKEN),
        )

        assertTrue(server.requests.isEmpty())
    }

    @Test
    fun `a cleartext server URL is refused in a release build`() {
        // An MDM channel over plaintext is a policy anyone on the network can rewrite: unlock the
        // phone, lift bedtime, unsuspend everything — and the phone would have no way to know. The
        // exemption exists for loopback and the emulator, and it is off unless the build says so.
        val store = InMemoryCredentialStore()

        val refused = enroller(store, cleartextAllowed = false)
            .enroll(extras(serverUrl = "http://guard.example"))
        assertTrue((refused as EnrollResult.Misprovisioned).reason, refused.reason.contains("https"))

        val stillRefused = enroller(store, cleartextAllowed = true)
            .enroll(extras(serverUrl = "http://guard.example"))
        assertTrue(stillRefused.toString(), stillRefused is EnrollResult.Misprovisioned)

        assertTrue(server.requests.isEmpty())
    }

    @Test
    fun `a URL that is not a URL is misprovisioning`() {
        val store = InMemoryCredentialStore()

        val relative = enroller(store).enroll(extras(serverUrl = "guard.example/api"))
        assertTrue(relative.toString(), relative is EnrollResult.Misprovisioned)

        val nonsense = enroller(store).enroll(extras(serverUrl = "ht tp://guard.example"))
        assertTrue(nonsense.toString(), nonsense is EnrollResult.Misprovisioned)

        // Absolute, https, and names nobody. This is the case the host check exists for: the scheme
        // check accepts it, and `URL(...)` would throw somewhere much less informative.
        val hostless = enroller(store).enroll(extras(serverUrl = "https:///api"))
        assertTrue(
            (hostless as EnrollResult.Misprovisioned).reason,
            hostless.reason.contains("host"),
        )

        assertTrue(server.requests.isEmpty())
    }

    @Test
    fun `an https URL is accepted without the cleartext exemption`() {
        // The negative control for the two tests above: they would also pass if every URL were
        // refused, which would be a device that can never enroll at all.
        val store = InMemoryCredentialStore()
        val https = "https://127.0.0.1:${server.baseUrl.substringAfterLast(':')}"

        val result = enroller(store, cleartextAllowed = false).enroll(extras(serverUrl = https))

        // It got as far as the network — TLS to a plaintext server fails, and that is a *deferred*
        // outcome, not a rejected URL.
        assertTrue(result.toString(), result is EnrollResult.Deferred)
    }

    private companion object {
        const val SUCCESS = """
            {"device_token":"dt","device_id":"dev-1","child_id":"child-1",
             "recovery":{"salt":"c2FsdA==","iterations":600000,"hash":"aGFzaA=="}}
        """
    }
}
