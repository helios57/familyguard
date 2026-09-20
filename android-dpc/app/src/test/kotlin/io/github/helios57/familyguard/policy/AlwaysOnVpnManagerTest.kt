package io.github.helios57.familyguard.policy

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * The one switch in this project that can leave a phone with no way back.
 *
 * Every test here is about lockdown or about the read-back. `setAlwaysOnVpnPackage` succeeding is
 * not the interesting claim — a device that accepts the call and comes back holding nothing, or
 * holding it *with lockdown on*, is a phone that either does not filter or cannot be fixed, and
 * neither of those raises anything.
 */
class AlwaysOnVpnManagerTest {

    private val own = "io.github.helios57.familyguard"

    @Test
    fun `switching on names this app and never enables lockdown`() {
        val gateway = FakeGateway()

        val outcome = manager(gateway).apply(wanted = true)

        assertTrue(outcome.toString(), outcome.ok)
        assertEquals(own, gateway.packageName)
        assertFalse("lockdown was enabled", gateway.lockdown)
        assertEquals(listOf(own to false), gateway.calls)
    }

    @Test
    fun `the constant this class passes is false`() {
        // Asserted directly as well as through the calls above, because it is the single value in
        // this project whose wrong setting is unrecoverable from the phone.
        assertFalse(AlwaysOnVpnManager.LOCKDOWN)
    }

    @Test
    fun `switching off clears the package`() {
        val gateway = FakeGateway(packageName = own)

        val outcome = manager(gateway).apply(wanted = false)

        assertTrue(outcome.toString(), outcome.ok)
        assertNull(gateway.packageName)
        assertEquals(listOf(null to false), gateway.calls)
    }

    @Test
    fun `switching off clears a package that is not ours either`() {
        val gateway = FakeGateway(packageName = "com.example.someothervpn")

        manager(gateway).apply(wanted = false)

        // Nothing else should ever hold always-on on a device this app owns, and a foreign package
        // there is a state a parent can neither see nor undo.
        assertNull(gateway.packageName)
    }

    @Test
    fun `an unchanged state is not re-applied`() {
        val gateway = FakeGateway(packageName = own, lockdown = false)

        val outcome = manager(gateway).apply(wanted = true)

        assertTrue(outcome.ok)
        assertTrue("the platform was called for no reason", gateway.calls.isEmpty())
        assertTrue(outcome.summary.contains("unchanged"))
    }

    @Test
    fun `lockdown found on is turned off even when the package already matches`() {
        val gateway = FakeGateway(packageName = own, lockdown = true)

        val outcome = manager(gateway).apply(wanted = true)

        // The only path in this project that clears it. An "unchanged" shortcut that looked at the
        // package alone would leave a phone one crash away from having no network.
        assertEquals(listOf(own to false), gateway.calls)
        assertFalse(gateway.lockdown)
        assertTrue(outcome.toString(), outcome.ok)
    }

    @Test
    fun `a device that comes back with lockdown on is a reported problem`() {
        val gateway = object : FakeGateway() {
            override fun setPackage(packageName: String?, lockdown: Boolean) {
                super.setPackage(packageName, lockdown)
                this.lockdown = true
            }
        }

        val outcome = manager(gateway).apply(wanted = true)

        assertFalse(outcome.ok)
        assertTrue(outcome.failure!!.contains("lockdown ON"))
    }

    @Test
    fun `a device that accepts the call and holds nothing is a reported problem`() {
        val gateway = object : FakeGateway() {
            override fun setPackage(packageName: String?, lockdown: Boolean) {
                calls += packageName to lockdown
                // Accepted, and nothing changed. Nothing throws, and the filter is off.
            }
        }

        val outcome = manager(gateway).apply(wanted = true)

        assertFalse(outcome.ok)
        assertTrue(outcome.failure!!.contains("reports none"))
    }

    @Test
    fun `a platform that refuses always-on is reported, not thrown`() {
        val gateway = object : FakeGateway() {
            override fun setPackage(packageName: String?, lockdown: Boolean) {
                throw UnsupportedOperationException("always-on is not supported on this device")
            }
        }

        val outcome = manager(gateway).apply(wanted = true)

        assertFalse(outcome.ok)
        assertTrue(outcome.failure!!.contains("not supported"))
    }

    @Test
    fun `a lockdown state that cannot be read is treated as needing a re-set`() {
        val gateway = object : FakeGateway(packageName = own) {
            var reads = 0
            override fun isLockdownEnabled(): Boolean {
                reads++
                // The first read is the one the "unchanged" shortcut depends on.
                if (reads == 1) throw SecurityException("not the owner right now")
                return false
            }
        }

        manager(gateway).apply(wanted = true)

        // Unknown is the one value that must not be trusted: an unreadable lockdown state that was
        // assumed false is the same phone as one where it is true.
        assertEquals(listOf(own to false), gateway.calls)
    }

    private fun manager(gateway: AlwaysOnVpnGateway) = AlwaysOnVpnManager(gateway, own)

    private open class FakeGateway(
        var packageName: String? = null,
        var lockdown: Boolean = false,
    ) : AlwaysOnVpnGateway {
        val calls = mutableListOf<Pair<String?, Boolean>>()

        override fun packageName(): String? = packageName
        override fun isLockdownEnabled(): Boolean = lockdown

        override fun setPackage(packageName: String?, lockdown: Boolean) {
            calls += packageName to lockdown
            this.packageName = packageName
            this.lockdown = lockdown
        }
    }
}
