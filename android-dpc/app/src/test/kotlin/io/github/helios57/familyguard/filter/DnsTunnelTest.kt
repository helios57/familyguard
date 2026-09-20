package io.github.helios57.familyguard.filter

import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.ByteArrayOutputStream

/**
 * One packet in, one decision out — driven by fixtures, with no tunnel and no socket in sight.
 *
 * The case worth naming is `an answer that arrives after the buffer was reused`: the tunnel reads
 * into one array forever, and a relayed answer comes back long after that array has been
 * overwritten. A tunnel that kept the reference instead of copying would address the reply from
 * whatever packet happened to be in the buffer at the time, and the answer would be delivered to
 * the wrong app — intermittently, under load, and never in a way a test that relays one packet at
 * a time would notice.
 */
class DnsTunnelTest {

    private val written = mutableListOf<ByteArray>()
    private val relayed = mutableListOf<ByteArray>()
    private var deliver: ((ByteArray) -> Unit)? = null

    private val relay = object : DnsRelay {
        override fun relay(query: ByteArray, onAnswer: (ByteArray) -> Unit) {
            relayed += query
            deliver = onAnswer
        }
    }

    @Test
    fun `a blocked name is answered on the spot, and never reaches a resolver`() {
        val tunnel = tunnel("||ads.example.com^")

        val outcome = tunnel.handleWhole(queryPacket("ads.example.com"))

        assertEquals(TunnelOutcome.Answered("ads.example.com"), outcome)
        assertTrue("nothing went upstream", relayed.isEmpty())
        assertEquals(1, written.size)
        assertEquals("name error", 3, dnsOf(written[0]).let { u16(it, 2) and 0x000F })
        assertEquals("the id the app is waiting for", 0x1234, u16(dnsOf(written[0]), 0))
    }

    @Test
    fun `an allowed name goes upstream, and nothing is written until the answer comes back`() {
        val tunnel = tunnel("||ads.example.com^")

        val outcome = tunnel.handleWhole(queryPacket("example.com"))

        assertEquals(TunnelOutcome.Relayed("example.com"), outcome)
        assertEquals(1, relayed.size)
        assertTrue("no answer invented in the meantime", written.isEmpty())
    }

    /**
     * The tunnel reads into one array forever. By the time a real resolver answers, that array
     * holds some other app's packet — so the reply has to be built from a copy taken at the time,
     * not from the buffer.
     */
    @Test
    fun `an answer that arrives after the buffer was reused still goes to the right app`() {
        val tunnel = tunnel("||ads.example.com^")
        val buffer = ByteArray(1500)
        val first = queryPacket("example.com", sourcePort = 40001, sourceAddress = byteArrayOf(10, 0, 0, 2))
        System.arraycopy(first, 0, buffer, 0, first.size)

        tunnel.handle(buffer, first.size)

        // The tunnel moves on: the same array now holds a completely different packet.
        val second = queryPacket("other.example.org", sourcePort = 49999, sourceAddress = byteArrayOf(10, 0, 0, 9))
        java.util.Arrays.fill(buffer, 0)
        System.arraycopy(second, 0, buffer, 0, second.size)

        deliver!!(byteArrayOf(0x12, 0x34, 0x81.toByte(), 0x80.toByte(), 0, 1, 0, 0, 0, 0, 0, 0))

        val reply = written.single()
        val replyIp = IpPacket.parse(reply)!!
        val replyUdp = TransportHeader.udp(reply, replyIp)!!
        assertEquals("addressed to the app that asked, not the one in the buffer", 40001, replyUdp.destinationPort)
        assertArrayEquals(
            byteArrayOf(10, 0, 0, 2),
            reply.copyOfRange(replyIp.destinationOffset, replyIp.destinationOffset + 4),
        )
    }

    @Test
    fun `the question handed upstream is the question the app asked`() {
        val tunnel = tunnel("||ads.example.com^")
        val packet = queryPacket("example.com")

        tunnel.handleWhole(packet)

        val ip = IpPacket.parse(packet)!!
        val udp = TransportHeader.udp(packet, ip)!!
        assertArrayEquals(
            packet.copyOfRange(udp.payloadOffset, udp.payloadOffset + udp.payloadLength),
            relayed.single(),
        )
    }

    // ---- everything that is left alone ------------------------------------------------------

    @Test
    fun `a packet that is not DNS is passed, not dropped`() {
        val tunnel = tunnel("||ads.example.com^")

        val outcome = tunnel.handleWhole(queryPacket("ads.example.com", destinationPort = 443))

        assertEquals(TunnelOutcome.Passed("not port 53"), outcome)
        assertTrue(written.isEmpty() && relayed.isEmpty())
    }

    @Test
    fun `a malformed query is passed, not answered`() {
        val tunnel = tunnel("||ads.example.com^")
        val packet = queryPacket("ads.example.com")
        val ip = IpPacket.parse(packet)!!
        val udp = TransportHeader.udp(packet, ip)!!
        packet[udp.payloadOffset + 4] = 9 // QDCOUNT 9, which this cannot answer

        val outcome = tunnel.handleWhole(packet)

        assertEquals(TunnelOutcome.Passed("not a single-question query"), outcome)
        assertTrue(written.isEmpty())
    }

    @Test
    fun `garbage is passed, not dropped`() {
        val tunnel = tunnel("||ads.example.com^")

        assertEquals(TunnelOutcome.Passed("not an IP packet"), tunnel.handle(ByteArray(40) { 0x7F }, 40))
    }

    @Test
    fun `with the filter off every name is relayed`() {
        val tunnel = tunnel("||ads.example.com^", enabled = false)

        assertEquals(TunnelOutcome.Relayed("ads.example.com"), tunnel.handleWhole(queryPacket("ads.example.com")))
    }

    @Test
    fun `an IPv6 query is answered as an IPv6 packet`() {
        val tunnel = tunnel("||ads.example.com^")

        val outcome = tunnel.handleWhole(queryPacket("ads.example.com", ipv6 = true))

        assertEquals(TunnelOutcome.Answered("ads.example.com"), outcome)
        assertEquals("version 6", 6, (written.single()[0].toInt() and 0xF0) ushr 4)
    }

    // ---- fixtures ---------------------------------------------------------------------------

    private fun tunnel(vararg rules: String, enabled: Boolean = true): DnsTunnel {
        val (index, _) = FilterCompiler.compile(rules.asSequence())
        val engine = FilterEngine(index, FilterEngine.neverBlockedFor("https://guard.example.com"), enabled)
        return DnsTunnel(engine, relay) { written += it }
    }

    /** Named differently from the member on purpose: an extension with the SAME signature as a
     *  member is never called — the member always wins, and the call silently gets -1.  */
    private fun DnsTunnel.handleWhole(packet: ByteArray) = handle(packet, packet.size)

    private fun dnsOf(packet: ByteArray): ByteArray {
        val ip = IpPacket.parse(packet)!!
        val udp = TransportHeader.udp(packet, ip)!!
        return packet.copyOfRange(udp.payloadOffset, udp.payloadOffset + udp.payloadLength)
    }

    private fun queryPacket(
        name: String,
        sourcePort: Int = 40001,
        destinationPort: Int = 53,
        sourceAddress: ByteArray = byteArrayOf(10, 0, 0, 2),
        ipv6: Boolean = false,
    ): ByteArray {
        val dns = ByteArrayOutputStream()
        dns.write(byteArrayOf(0x12, 0x34, 0x01, 0x00, 0, 1, 0, 0, 0, 0, 0, 0))
        for (label in name.split('.')) {
            dns.write(label.length)
            dns.write(label.toByteArray(Charsets.US_ASCII))
        }
        dns.write(0)
        dns.write(byteArrayOf(0, 1, 0, 1))
        val payload = dns.toByteArray()

        val udp = ByteArrayOutputStream()
        udp.write(u16b(sourcePort))
        udp.write(u16b(destinationPort))
        udp.write(u16b(8 + payload.size))
        udp.write(u16b(0))
        udp.write(payload)
        val segment = udp.toByteArray()

        val out = ByteArrayOutputStream()
        if (ipv6) {
            out.write(byteArrayOf(0x60, 0, 0, 0))
            out.write(u16b(segment.size))
            out.write(IpHeader.PROTO_UDP)
            out.write(64)
            out.write(ByteArray(16) { if (it == 15) 2 else 0x20 })
            out.write(ByteArray(16) { if (it == 15) 1 else 0x20 })
        } else {
            out.write(0x45)
            out.write(0)
            out.write(u16b(20 + segment.size))
            out.write(u16b(0x1234))
            out.write(u16b(0x4000))
            out.write(64)
            out.write(IpHeader.PROTO_UDP)
            out.write(u16b(0))
            out.write(sourceAddress)
            out.write(byteArrayOf(10, 0, 0, 1))
        }
        out.write(segment)
        return out.toByteArray()
    }

    private fun u16(b: ByteArray, at: Int) = ((b[at].toInt() and 0xFF) shl 8) or (b[at + 1].toInt() and 0xFF)

    private fun u16b(value: Int) = byteArrayOf((value shr 8).toByte(), value.toByte())
}
