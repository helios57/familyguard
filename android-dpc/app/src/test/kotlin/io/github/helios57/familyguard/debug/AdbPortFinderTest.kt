package io.github.helios57.familyguard.debug

import java.net.InetAddress
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * Which mDNS announcement counts as this phone's adbd (FR-19.3).
 *
 * The rest of [AdbPortFinder] is NsdManager callbacks and a deadline, which need a phone. This is
 * the one rule in it, and the one whose failure would relay a child's debug session somewhere else.
 */
class AdbPortFinderTest {

    private val wifi = InetAddress.getByName("192.168.1.20")
    private val linkLocal = InetAddress.getByName("fe80::1c2d:3eff:fe4f:5a6b")
    private val own = setOf(wifi, linkLocal, InetAddress.getByName("127.0.0.1"))

    @Test
    fun `an announcement from one of this phone's addresses is accepted`() {
        assertTrue(AdbPortFinder.isThisPhones(AdbPortFinder.Found(wifi, 37099), own))
        assertTrue(AdbPortFinder.isThisPhones(AdbPortFinder.Found(linkLocal, 37099), own))
    }

    @Test
    fun `the same service announced by another device on the network is refused`() {
        val laptop = InetAddress.getByName("192.168.1.21")
        assertFalse(AdbPortFinder.isThisPhones(AdbPortFinder.Found(laptop, 37099), own))
    }

    @Test
    fun `a phone with no addresses accepts nothing`() {
        assertFalse(AdbPortFinder.isThisPhones(AdbPortFinder.Found(wifi, 37099), emptySet()))
    }

    @Test
    fun `a port outside TCP's range is not a port`() {
        assertFalse("port 0 was accepted", AdbPortFinder.isThisPhones(AdbPortFinder.Found(wifi, 0), own))
        assertFalse("port 65536 was accepted", AdbPortFinder.isThisPhones(AdbPortFinder.Found(wifi, 65536), own))
        assertTrue("port 1 was refused", AdbPortFinder.isThisPhones(AdbPortFinder.Found(wifi, 1), own))
        assertTrue("port 65535 was refused", AdbPortFinder.isThisPhones(AdbPortFinder.Found(wifi, 65535), own))
    }
}
