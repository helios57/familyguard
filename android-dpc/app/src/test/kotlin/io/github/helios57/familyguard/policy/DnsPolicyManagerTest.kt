package io.github.helios57.familyguard.policy

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * @param accept what a set call returns.
 * @param applyIt when false the call reports [accept] and changes nothing — the accepted-and-not-in-
 * effect shape the read-back exists to catch.
 */
private class FakeDnsGateway(
    private var mode: PrivateDnsMode = PrivateDnsMode.OPPORTUNISTIC,
    private var host: String? = null,
    private val accept: PrivateDnsResult = PrivateDnsResult.OK,
    private val applyIt: Boolean = true,
    private val throws: RuntimeException? = null,
) : DnsGateway {
    var setCalls = 0
        private set

    override fun mode(): PrivateDnsMode = mode

    override fun host(): String? = host

    override fun setSpecifiedHost(host: String): PrivateDnsResult {
        setCalls++
        throws?.let { throw it }
        if (accept == PrivateDnsResult.OK && applyIt) {
            mode = PrivateDnsMode.HOSTNAME
            this.host = host
        }
        return accept
    }

    override fun setOpportunistic(): PrivateDnsResult {
        setCalls++
        throws?.let { throw it }
        if (accept == PrivateDnsResult.OK && applyIt) {
            mode = PrivateDnsMode.OPPORTUNISTIC
            host = null
        }
        return accept
    }
}

class DnsPolicyManagerTest {

    @Test
    fun `sets the host the policy asks for`() {
        val gateway = FakeDnsGateway(mode = PrivateDnsMode.OFF)

        val outcome = DnsPolicyManager(gateway).apply("family.dns.example")

        assertTrue(outcome.toString(), outcome.ok)
        assertEquals(PrivateDnsMode.HOSTNAME, gateway.mode())
        assertEquals("family.dns.example", gateway.host())
    }

    @Test
    fun `an empty host means opportunistic, never off`() {
        val gateway = FakeDnsGateway(mode = PrivateDnsMode.HOSTNAME, host = "old.example")

        val outcome = DnsPolicyManager(gateway).apply("")

        assertTrue(outcome.toString(), outcome.ok)
        // OFF would hand the child's queries to whatever resolver the café Wi-Fi advertises —
        // strictly worse than the phone's default, arrived at by a policy trying to be neutral.
        assertEquals(PrivateDnsMode.OPPORTUNISTIC, gateway.mode())
    }

    @Test
    fun `a host already in effect is not re-applied`() {
        val gateway = FakeDnsGateway(mode = PrivateDnsMode.HOSTNAME, host = "family.dns.example")

        val outcome = DnsPolicyManager(gateway).apply("family.dns.example")

        assertTrue(outcome.toString(), outcome.ok)
        // The platform probes the host over the network before applying, so re-applying on every
        // sync is a DoT probe every fifteen minutes — and one that fails in a tunnel reports a
        // problem for a device that is correctly configured.
        assertEquals(0, gateway.setCalls)
        assertTrue(outcome.summary.contains("unchanged"))
    }

    @Test
    fun `opportunistic already in effect is not re-applied`() {
        val gateway = FakeDnsGateway(mode = PrivateDnsMode.OPPORTUNISTIC)

        val outcome = DnsPolicyManager(gateway).apply("   ")

        assertTrue(outcome.toString(), outcome.ok)
        assertEquals(0, gateway.setCalls)
    }

    @Test
    fun `a host that does not answer DNS-over-TLS is a failure that names the host`() {
        val gateway = FakeDnsGateway(
            mode = PrivateDnsMode.OPPORTUNISTIC,
            accept = PrivateDnsResult.HOST_NOT_SERVING,
        )

        val outcome = DnsPolicyManager(gateway).apply("typo.example")

        assertFalse(outcome.ok)
        val failure = requireNotNull(outcome.failure) { "expected a failure, got $outcome" }
        assertTrue(failure, failure.contains("typo.example"))
        assertTrue(failure, failure.contains("does not answer DNS-over-TLS"))
        // The device kept the resolver it had. A typo in the console must not take the phone off the
        // network.
        assertEquals(PrivateDnsMode.OPPORTUNISTIC, gateway.mode())
    }

    @Test
    fun `a refusal is a failure`() {
        val gateway = FakeDnsGateway(mode = PrivateDnsMode.OFF, accept = PrivateDnsResult.FAILED)

        val outcome = DnsPolicyManager(gateway).apply("family.dns.example")

        assertFalse(outcome.ok)
        assertEquals("the platform refused to set the private DNS host", outcome.failure)
    }

    @Test
    fun `accepted and not in effect is a failure`() {
        val gateway = FakeDnsGateway(mode = PrivateDnsMode.OPPORTUNISTIC, applyIt = false)

        val outcome = DnsPolicyManager(gateway).apply("family.dns.example")

        // The return code said yes. The read says the device is still resolving through whatever it
        // was before — a console showing a configured filter over a phone that is filtering nothing.
        assertFalse(outcome.ok)
        assertTrue(outcome.failure!!.startsWith("accepted, and the device reports"))
    }

    @Test
    fun `an accepted clear that did not take effect is a failure`() {
        val gateway = FakeDnsGateway(mode = PrivateDnsMode.HOSTNAME, host = "old.example", applyIt = false)

        val outcome = DnsPolicyManager(gateway).apply("")

        assertFalse(outcome.ok)
        assertTrue(outcome.failure!!.startsWith("accepted, and the device reports"))
    }

    @Test
    fun `a throwing platform is a failure carrying its message`() {
        val gateway = FakeDnsGateway(
            mode = PrivateDnsMode.OFF,
            throws = SecurityException("not the device owner"),
        )

        val outcome = DnsPolicyManager(gateway).apply("family.dns.example")

        assertFalse(outcome.ok)
        assertEquals("not the device owner", outcome.failure)
    }

    @Test
    fun `whitespace around a host is trimmed rather than sent to the platform`() {
        val gateway = FakeDnsGateway(mode = PrivateDnsMode.OFF)

        val outcome = DnsPolicyManager(gateway).apply("  family.dns.example  ")

        assertTrue(outcome.toString(), outcome.ok)
        assertEquals("family.dns.example", gateway.host())
    }
}
