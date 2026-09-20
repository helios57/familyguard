package io.github.helios57.familyguard.filter

import java.io.ByteArrayOutputStream
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * The routing table, asserted as a table: what happens to a packet of each shape.
 *
 * This class is the filter's whole decision surface, and the way it fails is by *quietly* not
 * carrying something. A dropped protocol has no error, no log line on the phone and no symptom a
 * child can describe beyond "the internet is weird" — so every row below asserts both what is
 * dropped and what is not, and the two QUIC cases are deliberately a pair: a test that only watches
 * the drop would stay green if the router dropped everything.
 */
class PacketRouterTest {

    private val client = byteArrayOf(10, 0, 0, 2)
    private val destination = byteArrayOf(93, 184.toByte(), 216.toByte(), 34)
    private val resolver = byteArrayOf(10, 0, 0, 1)

    private val written = mutableListOf<ByteArray>()
    private val opener = RecordingOpener()
    private val relayed = mutableListOf<ByteArray>()

    // ---- what each mode carries -----------------------------------------------------------------

    /**
     * The safety property of `DNS_ONLY`: a defect in the TCP machine can cost DNS and nothing else,
     * because nothing else was ever routed here.
     */
    @Test
    fun `in DNS-only mode a TCP segment is dropped and no socket is opened`() {
        val router = router(mode = RouteMode.DNS_ONLY)

        router.handle(syn())

        assertEquals(1, router.counters().dropped)
        assertEquals(0, router.counters().flowsOpened)
        assertTrue("nothing may be dialled in a mode that routes only the resolver", opener.tcp.isEmpty())
        assertTrue(written.isEmpty())
    }

    @Test
    fun `in DNS-only mode a datagram that is not for the resolver is dropped`() {
        val router = router(mode = RouteMode.DNS_ONLY)

        router.handle(datagram(port = 123, payload = ByteArray(8)))

        assertEquals(1, router.counters().dropped)
        assertTrue(opener.udp.isEmpty())
    }

    @Test
    fun `a query for the resolver reaches the DNS tunnel in either mode`() {
        for (mode in RouteMode.entries) {
            written.clear()
            val router = router(mode = mode, rules = listOf("||ads.example.com^"))

            router.handle(dnsQuery("ads.example.com"))

            val answer = written.singleOrNull()
            assertNotNull("$mode must still answer DNS; it is the only thing DNS_ONLY routes", answer)
            assertEquals("$mode must answer NXDOMAIN for a blocked name", NXDOMAIN, rcodeOf(answer!!))
        }
    }

    // ---- QUIC, both halves ------------------------------------------------------------------------

    /**
     * Dropped on purpose, and it is the one deliberate breakage in the whole filter. A QUIC
     * handshake encrypts the name; every client treats an unanswered one as "this network does not
     * do QUIC" and falls back within a few hundred milliseconds to TLS over TCP, where the name is
     * in clear text. Carrying it instead would exempt Google's ad libraries, which reach for QUIC
     * first — i.e. exactly the traffic this feature exists for.
     */
    @Test
    fun `QUIC is dropped so the client falls back to a handshake the name can be read from`() {
        val router = router(mode = RouteMode.FULL)

        router.handle(datagram(port = PacketRouter.QUIC_PORT, payload = ByteArray(1200)))

        assertEquals(1, router.counters().quicDropped)
        assertEquals(0, router.counters().udpRelayed)
        assertTrue("a dropped datagram must not also open a socket", opener.udp.isEmpty())
    }

    /** The other half: without the switch the same datagram is carried, so the drop is the rule. */
    @Test
    fun `with QUIC dropping off the very same datagram is relayed`() {
        val router = router(mode = RouteMode.FULL, dropQuic = false)

        router.handle(datagram(port = PacketRouter.QUIC_PORT, payload = ByteArray(1200)))

        assertEquals(0, router.counters().quicDropped)
        assertEquals(1, router.counters().udpRelayed)
        assertEquals(1, opener.udp.size)
    }

    @Test
    fun `an ordinary datagram is relayed and its reply comes back addressed to the app`() {
        val router = router(mode = RouteMode.FULL)
        val payload = byteArrayOf(1, 2, 3, 4)

        router.handle(datagram(port = 3478, payload = payload))
        val relay = opener.udp.single()
        assertBytes("the payload must be what the app sent", payload, relay.connection.sent.single())

        relay.listener.onData(byteArrayOf(9, 9), 0, 2)

        val reply = written.single()
        val ip = IpPacket.parse(reply)!!
        val udp = TransportHeader.udp(reply, ip)!!
        assertEquals("the reply must appear to come from the destination", 3478, udp.sourcePort)
        assertEquals(CLIENT_PORT, udp.destinationPort)
        assertBytes("", byteArrayOf(9, 9), reply.copyOfRange(udp.payloadOffset, udp.payloadOffset + udp.payloadLength))
    }

    /** One socket per conversation, not one per packet: a phone would run out of descriptors. */
    @Test
    fun `a second datagram of the same conversation reuses the socket`() {
        val router = router(mode = RouteMode.FULL)

        router.handle(datagram(port = 3478, payload = byteArrayOf(1)))
        router.handle(datagram(port = 3478, payload = byteArrayOf(2)))

        assertEquals(1, opener.udp.size)
        assertEquals(2, opener.udp.single().connection.sent.size)
        assertEquals(2, router.counters().udpRelayed)
    }

    /**
     * UDP has no close, so without expiry every game packet leaves a descriptor behind for the life
     * of the tunnel — and running out looks exactly like the network failing.
     */
    @Test
    fun `a relay nothing has used is closed, and a busy one is not`() {
        var now = 1_000L
        val router = router(mode = RouteMode.FULL, clock = { now })
        router.handle(datagram(port = 3478, payload = byteArrayOf(1)))
        router.handle(datagram(port = 3479, payload = byteArrayOf(1)))

        now += PacketRouter.UDP_IDLE_MILLIS + 1
        router.handle(datagram(port = 3479, payload = byteArrayOf(2))) // still in use
        val closed = router.expire()

        assertEquals(1, closed)
        assertTrue("the idle one must go", opener.udp.first().connection.closed)
        assertFalse("the busy one must not", opener.udp.last().connection.closed)
    }

    // ---- connections ------------------------------------------------------------------------------

    /**
     * A segment for a session that predates the tunnel. There is no state anywhere to continue it,
     * so ending it is the honest answer: dropping would leave the app waiting out its own timeout
     * on a connection nothing will ever answer.
     */
    @Test
    fun `a segment for a connection the tunnel never saw is reset rather than ignored`() {
        val router = router(mode = RouteMode.FULL)

        router.handle(
            tcp(flags = TcpHeader.FLAG_ACK or TcpHeader.FLAG_PSH, sequence = 5, payload = byteArrayOf(1)),
        )

        val reset = TransportHeader.tcp(written.single(), IpPacket.parse(written.single())!!)!!
        assertTrue(reset.isRst)
        assertEquals(0, router.openFlows())
    }

    @Test
    fun `a blocked name is counted, reset, and never dialled`() {
        val router = router(mode = RouteMode.FULL, rules = listOf("||ads.example.com^"))
        handshake(router)

        router.handle(tcp(TcpHeader.FLAG_ACK or TcpHeader.FLAG_PSH, 1, clientHello("ads.example.com")))

        assertEquals(1, router.counters().flowsBlocked)
        assertTrue("a blocked destination must not be contacted at all", opener.tcp.isEmpty())
        assertTrue(written.any { isReset(it) })
        assertEquals("a decided flow must not be left in the table", 0, router.openFlows())
    }

    @Test
    fun `an allowed name is dialled once and the hello reaches the destination`() {
        val router = router(mode = RouteMode.FULL, rules = listOf("||ads.example.com^"))
        handshake(router)
        val hello = clientHello("cdn.example.org")

        router.handle(tcp(TcpHeader.FLAG_ACK or TcpHeader.FLAG_PSH, 1, hello))

        val connection = opener.tcp.single()
        assertEquals(443, connection.port)
        assertBytes("the hello was acknowledged already; losing it hangs the flow", hello, connection.connection.sent.single())
    }

    /**
     * The hole this closes: `openTcp` is called while the flow is deciding, and the bytes it wants
     * to send have already been acknowledged to the app. A socket that appears one call later must
     * still receive them.
     */
    @Test
    fun `bytes decided on before the socket existed are not lost`() {
        val router = router(mode = RouteMode.FULL)
        handshake(router)
        opener.deferConnect = true
        val hello = clientHello("cdn.example.org")

        router.handle(tcp(TcpHeader.FLAG_ACK or TcpHeader.FLAG_PSH, 1, hello))
        router.handle(tcp(TcpHeader.FLAG_ACK or TcpHeader.FLAG_PSH, 1 + hello.size, byteArrayOf(7, 7)))

        val sent = opener.tcp.single().connection.sent
        assertBytes("the hello must arrive first", hello, sent.first())
        assertBytes("and what followed it, in order", byteArrayOf(7, 7), sent.last())
    }

    @Test
    fun `a destination that cannot be dialled is reported to the app as a reset`() {
        val router = router(mode = RouteMode.FULL)
        opener.refuseTcp = true
        handshake(router)

        router.handle(tcp(TcpHeader.FLAG_ACK or TcpHeader.FLAG_PSH, 1, clientHello("cdn.example.org")))

        assertEquals(1, router.counters().upstreamFailures)
        assertTrue("a socket that cannot be opened must be an error, not a spinner", written.any { isReset(it) })
        assertEquals(0, router.openFlows())
    }

    @Test
    fun `closing the tunnel closes every socket it opened`() {
        val router = router(mode = RouteMode.FULL)
        handshake(router)
        router.handle(tcp(TcpHeader.FLAG_ACK or TcpHeader.FLAG_PSH, 1, clientHello("cdn.example.org")))
        router.handle(datagram(port = 3478, payload = byteArrayOf(1)))

        router.closeAll()

        assertTrue("no socket may outlive the tunnel", opener.tcp.single().connection.closed)
        assertTrue(opener.udp.single().connection.closed)
        assertEquals(0, router.openFlows())
    }

    @Test
    fun `a protocol with no name in it is dropped rather than guessed at`() {
        val router = router(mode = RouteMode.FULL)

        // ICMP. A VpnService cannot forward it without a raw socket, which apps do not get.
        router.handle(ipv4(protocol = IpHeader.PROTO_ICMP, payload = byteArrayOf(8, 0, 0, 0, 0, 0, 0, 0)))

        assertEquals(1, router.counters().dropped)
        assertEquals(1, router.counters().packets)
    }

    // ---- fixtures ---------------------------------------------------------------------------------

    private fun router(
        mode: RouteMode,
        rules: List<String> = emptyList(),
        dropQuic: Boolean = true,
        clock: () -> Long = { 1_000L },
    ): PacketRouter {
        val (index, _) = FilterCompiler.compile(rules.asSequence())
        val engine = FilterEngine(index, FilterEngine.neverBlockedFor("https://guard.example.com"), true)
        val relay = object : DnsRelay {
            override fun relay(query: ByteArray, onAnswer: (ByteArray) -> Unit) {
                relayed += query
            }
        }
        return PacketRouter(
            engine = engine,
            dnsTunnel = DnsTunnel(engine, relay) { written += it },
            opener = opener,
            toClient = { written += it },
            mode = { mode },
            dropQuic = dropQuic,
            clock = clock,
        )
    }

    private fun PacketRouter.handle(packet: ByteArray) = handle(packet, packet.size)

    private fun handshake(router: PacketRouter) {
        router.handle(syn())
        val synAck = TransportHeader.tcp(written.last(), IpPacket.parse(written.last())!!)!!
        router.handle(tcp(TcpHeader.FLAG_ACK, 1, acknowledgement = synAck.sequenceNumber + 1))
        written.clear()
    }

    private fun syn() = tcp(TcpHeader.FLAG_SYN, 0)

    private fun tcp(
        flags: Int,
        sequence: Int,
        payload: ByteArray = ByteArray(0),
        acknowledgement: Long = 0,
        port: Int = 443,
    ): ByteArray = PacketBuilder.tcpSegment(
        version = 4,
        sourceAddress = client,
        destinationAddress = destination,
        sourcePort = CLIENT_PORT,
        destinationPort = port,
        sequence = (CLIENT_ISN + sequence).toLong(),
        acknowledgement = acknowledgement,
        flags = flags,
        window = 65535,
        payload = payload,
    )

    private fun datagram(port: Int, payload: ByteArray): ByteArray = PacketBuilder.udpDatagram(
        version = 4,
        sourceAddress = client,
        destinationAddress = destination,
        sourcePort = CLIENT_PORT,
        destinationPort = port,
        payload = payload,
    )

    private fun dnsQuery(name: String): ByteArray {
        val dns = ByteArrayOutputStream()
        dns.write(byteArrayOf(0x12, 0x34, 0x01, 0x00, 0, 1, 0, 0, 0, 0, 0, 0))
        for (label in name.split('.')) {
            dns.write(label.length)
            dns.write(label.toByteArray(Charsets.US_ASCII))
        }
        dns.write(0)
        dns.write(byteArrayOf(0, 1, 0, 1))
        return PacketBuilder.udpDatagram(
            version = 4,
            sourceAddress = client,
            destinationAddress = resolver,
            sourcePort = CLIENT_PORT,
            destinationPort = 53,
            payload = dns.toByteArray(),
        )
    }

    /** An IP packet carrying something that is neither TCP nor UDP. */
    private fun ipv4(protocol: Int, payload: ByteArray): ByteArray {
        val out = ByteArrayOutputStream()
        out.write(0x45)
        out.write(0)
        out.write(byteArrayOf((((20 + payload.size) shr 8).toByte()), (20 + payload.size).toByte()))
        out.write(byteArrayOf(0x12, 0x34, 0x40, 0x00))
        out.write(64)
        out.write(protocol)
        out.write(byteArrayOf(0, 0))
        out.write(client)
        out.write(destination)
        out.write(payload)
        return out.toByteArray()
    }

    private fun isReset(packet: ByteArray): Boolean {
        val ip = IpPacket.parse(packet) ?: return false
        return TransportHeader.tcp(packet, ip)?.isRst == true
    }

    private fun rcodeOf(packet: ByteArray): Int {
        val ip = IpPacket.parse(packet)!!
        val udp = TransportHeader.udp(packet, ip)!!
        return packet[udp.payloadOffset + 3].toInt() and 0x0F
    }

    private fun assertEquals(expected: Int, actual: Long) =
        org.junit.Assert.assertEquals(expected.toLong(), actual)

    private fun assertBytes(message: String, expected: ByteArray, actual: ByteArray) =
        org.junit.Assert.assertEquals(message, expected.toList(), actual.toList())

    private fun clientHello(serverName: String): ByteArray {
        val name = serverName.toByteArray(Charsets.US_ASCII)
        val entry = bytes { write(0); write(u16(name.size)); write(name) }
        val list = bytes { write(u16(entry.size)); write(entry) }
        val extensions = bytes { write(u16(0x0000)); write(u16(list.size)); write(list) }
        val body = bytes {
            write(byteArrayOf(0x03, 0x03))
            write(ByteArray(32))
            write(0)
            write(u16(2))
            write(byteArrayOf(0x13, 0x01))
            write(1)
            write(0)
            write(u16(extensions.size))
            write(extensions)
        }
        val handshake = bytes {
            write(0x01)
            write(byteArrayOf((body.size shr 16).toByte(), (body.size shr 8).toByte(), body.size.toByte()))
            write(body)
        }
        return bytes { write(byteArrayOf(0x16, 0x03, 0x01)); write(u16(handshake.size)); write(handshake) }
    }

    private fun bytes(build: ByteArrayOutputStream.() -> Unit): ByteArray =
        ByteArrayOutputStream().apply(build).toByteArray()

    private fun u16(value: Int) = byteArrayOf((value shr 8).toByte(), value.toByte())

    /** Records every socket the router asks for, and lets a test drive it from the far end. */
    private class RecordingOpener : UpstreamOpener {
        class Opened(val port: Int, val connection: FakeUpstream, val listener: UpstreamListener)

        val tcp = mutableListOf<Opened>()
        val udp = mutableListOf<Opened>()

        /** Makes `openTcp` succeed but stay silent, as a real non-blocking connect does. */
        var deferConnect = false
        var refuseTcp = false

        override fun openTcp(address: ByteArray, port: Int, listener: UpstreamListener): Upstream? {
            if (refuseTcp) return null
            val connection = FakeUpstream()
            tcp += Opened(port, connection, listener)
            if (!deferConnect) listener.onConnected()
            return connection
        }

        override fun openUdp(address: ByteArray, port: Int, listener: UpstreamListener): Upstream {
            val connection = FakeUpstream()
            udp += Opened(port, connection, listener)
            return connection
        }
    }

    private class FakeUpstream : Upstream {
        val sent = mutableListOf<ByteArray>()
        var sendClosed = false
        var closed = false

        override fun send(bytes: ByteArray) {
            sent += bytes
        }

        override fun closeSend() {
            sendClosed = true
        }

        override fun close() {
            closed = true
        }
    }

    private companion object {
        const val CLIENT_PORT = 40000
        const val CLIENT_ISN = 1_000_000
        const val NXDOMAIN = 3
    }
}
