package io.github.helios57.familyguard.filter

import org.junit.Assert.assertEquals
import org.junit.Test
import java.net.InetAddress

/**
 * One entry per network, and the one the phone is actually on.
 *
 * The first test is the defect the family hit: the filter was on, 180423 rules were compiled, the
 * phone was on Wi-Fi with a live heartbeat, and the tunnel reported *"the network offers no
 * resolver to forward queries to"* for hours. The bookkeeping it replaces emptied the whole list on
 * any network's loss, and nothing on the phone could refill it.
 */
class ResolverBookTest {

    private val wifi = "wifi"
    private val cell = "cellular"

    @Test
    fun `a network going away does not take another network's resolver with it`() {
        val book = ResolverBook<String>()
        book.learned(wifi, listOf("192.168.0.1"))
        book.learned(cell, listOf("10.0.0.1"))

        book.lost(cell)

        assertEquals(listOf("192.168.0.1"), book.forwardTo(wifi))
        // And with no idea which network is active — the state this is in whenever the tunnel is
        // up, because the active network is then the tunnel itself.
        assertEquals(listOf("192.168.0.1"), book.forwardTo(null))
    }

    @Test
    fun `the resolver of the network the phone is on is the one to forward to`() {
        val book = ResolverBook<String>()
        book.learned(wifi, listOf("192.168.0.1"))
        book.learned(cell, listOf("10.0.0.1"))

        assertEquals(listOf("10.0.0.1"), book.forwardTo(cell))
        assertEquals(listOf("192.168.0.1"), book.forwardTo(wifi))
    }

    @Test
    fun `a network that names no resolver cannot win over one that does`() {
        val book = ResolverBook<String>()
        book.learned(wifi, listOf("192.168.0.1"))
        book.learned(cell, emptyList())

        // Asked about the network with nothing, the answer is still the one that has something: a
        // stored empty entry would have been "forward nowhere", which is the tunnel not coming up.
        assertEquals(listOf("192.168.0.1"), book.forwardTo(cell))
    }

    @Test
    fun `a network that stops naming a resolver is forgotten rather than remembered as empty`() {
        val book = ResolverBook<String>()
        book.learned(wifi, listOf("192.168.0.1"))
        book.learned(wifi, emptyList())

        assertEquals(emptyList<String>(), book.forwardTo(wifi))
        assertEquals(emptyList<String>(), book.forwardTo(null))
    }

    @Test
    fun `nothing known is nothing to forward to`() {
        assertEquals(emptyList<String>(), ResolverBook<String>().forwardTo(wifi))
    }

    @Test
    fun `the tunnel's own address is never an upstream`() {
        val offered = listOf(
            InetAddress.getByName(AdFilterVpnService.TUNNEL_RESOLVER),
            InetAddress.getByName("192.168.0.1"),
        )
        // Forwarding a query to the address it arrived at is the loop that pegs the CPU until the
        // battery is flat, and the platform hands this address back for any network the tunnel is
        // already on.
        assertEquals(listOf("192.168.0.1"), ResolverBook.usable(offered, AdFilterVpnService.TUNNEL_RESOLVER))
    }

    @Test
    fun `an address is offered once, however often the platform names it`() {
        val offered = listOf(
            InetAddress.getByName("192.168.0.1"),
            InetAddress.getByName("192.168.0.1"),
            InetAddress.getByName("1.1.1.1"),
        )
        assertEquals(listOf("192.168.0.1", "1.1.1.1"), ResolverBook.usable(offered, "100.88.0.1"))
    }

    @Test
    fun `an IPv6 resolver is not offered, because reaching it is not proven`() {
        val offered = listOf(
            InetAddress.getByName("2001:4860:4860::8888"),
            InetAddress.getByName("192.168.0.1"),
        )
        assertEquals(listOf("192.168.0.1"), ResolverBook.usable(offered, "100.88.0.1"))

        // And a link with only those offers nothing at all, which stands the tunnel down. That is
        // the safe half of the trade: while the tunnel is up it IS the phone's resolver, so an
        // upstream it cannot reach costs the phone every name it looks up.
        assertEquals(emptyList<String>(), ResolverBook.usable(offered.take(1), "100.88.0.1"))
    }
}
