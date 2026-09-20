package io.github.helios57.familyguard.filter

import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.ByteArrayOutputStream

/**
 * The packets the filter makes up, checked against an independent implementation of the checksum.
 *
 * A checksum is the one field here whose wrongness has no symptom: the app's own stack discards the
 * packet without a word, the query goes unanswered, the app retries, and what a parent sees is a
 * game that takes ten seconds to open. So the oracle in this file is written separately from the
 * production code — and [`the oracle agrees with a published example`] pins the oracle itself
 * against RFC 1071's worked header, because two implementations of the same misunderstanding agree
 * perfectly.
 */
class PacketBuilderTest {

    /**
     * The IPv4 header from the standard worked example, checksum `0xB861`.
     *
     * Without this, every other assertion in the file compares the production code against test
     * code that was written the same afternoon by reading the same paragraph.
     */
    @Test
    fun `the oracle agrees with a published example`() {
        val header = byteArrayOf(
            0x45, 0x00, 0x00, 0x73, 0x00, 0x00, 0x40, 0x00,
            0x40, 0x11, 0x00, 0x00, // checksum zeroed
            0xC0.toByte(), 0xA8.toByte(), 0x00, 0x01,
            0xC0.toByte(), 0xA8.toByte(), 0x00, 0xC7.toByte(),
        )

        assertEquals(0xB861, onesComplement(header))
    }

    // ---- the DNS answer ---------------------------------------------------------------------

    @Test
    fun `the reply goes back to whoever asked`() {
        val request = ipv4Udp(source = 53000, destination = 53)
        val ip = IpPacket.parse(request)!!
        val udp = TransportHeader.udp(request, ip)!!

        val reply = PacketBuilder.udpReply(request, ip, udp, byteArrayOf(9, 9, 9))
        val replyIp = IpPacket.parse(reply)!!
        val replyUdp = TransportHeader.udp(reply, replyIp)!!

        assertEquals("from the port that was asked", 53, replyUdp.sourcePort)
        assertEquals("to the port that asked", 53000, replyUdp.destinationPort)
        assertArrayEquals(
            "from the address that was asked",
            request.copyOfRange(ip.destinationOffset, ip.destinationOffset + 4),
            reply.copyOfRange(replyIp.sourceOffset, replyIp.sourceOffset + 4),
        )
        assertArrayEquals(
            "to the address that asked",
            request.copyOfRange(ip.sourceOffset, ip.sourceOffset + 4),
            reply.copyOfRange(replyIp.destinationOffset, replyIp.destinationOffset + 4),
        )
        assertArrayEquals(byteArrayOf(9, 9, 9), reply.copyOfRange(replyUdp.payloadOffset, reply.size))
    }

    @Test
    fun `the IPv4 header checksum is right`() {
        val reply = udpReplyFor(ipv4Udp(53000, 53), byteArrayOf(9, 9, 9))

        assertIpv4HeaderChecksum(reply)
    }

    @Test
    fun `the IPv4 UDP checksum is right`() {
        val reply = udpReplyFor(ipv4Udp(53000, 53), byteArrayOf(9, 9, 9))

        assertTransportChecksum(reply, version = 4, protocol = IpHeader.PROTO_UDP, checksumAt = 20 + 6)
    }

    /**
     * The pseudo-header is the easy half to leave out, and leaving it out is invisible to any test
     * that only checks the payload arithmetic: the checksum is then correct about the bytes and
     * says nothing about who the packet is for.
     */
    @Test
    fun `the UDP checksum covers the addresses, not just the payload`() {
        val payload = byteArrayOf(9, 9, 9)
        val a = udpReplyFor(ipv4Udp(53000, 53), payload)
        val b = udpReplyFor(ipv4Udp(53000, 53, sourceAddress = byteArrayOf(10, 0, 0, 99)), payload)

        assertArrayEquals("same payload either way", payload, b.copyOfRange(28, b.size))
        assertNotEquals(
            "a different peer must give a different checksum",
            u16(a, 26),
            u16(b, 26),
        )
    }

    /** Most DNS answers are an odd number of bytes, so the pad side is exercised constantly. */
    @Test
    fun `an odd-length payload is checksummed with the pad on the right`() {
        val reply = udpReplyFor(ipv4Udp(53000, 53), byteArrayOf(1, 2, 3, 4, 5))

        assertTransportChecksum(reply, version = 4, protocol = IpHeader.PROTO_UDP, checksumAt = 20 + 6)
    }

    @Test
    fun `an IPv6 reply carries the right length, next header and checksum`() {
        val request = ipv6Udp(53000, 53)
        val ip = IpPacket.parse(request)!!
        val udp = TransportHeader.udp(request, ip)!!

        val reply = PacketBuilder.udpReply(request, ip, udp, byteArrayOf(9, 9, 9))

        assertEquals("version 6", 6, (reply[0].toInt() and 0xF0) ushr 4)
        assertEquals("payload length covers the UDP header too", 11, u16(reply, 4))
        assertEquals(IpHeader.PROTO_UDP, reply[6].toInt() and 0xFF)
        assertTransportChecksum(reply, version = 6, protocol = IpHeader.PROTO_UDP, checksumAt = 40 + 6)
    }

    /** End to end: a real query in, a real NXDOMAIN packet out, parsed back off the wire. */
    @Test
    fun `a blocked query comes back as a name error the app can read`() {
        val request = ipv4Udp(53000, 53, payload = dnsQuery("ads.example.com"))
        val ip = IpPacket.parse(request)!!
        val udp = TransportHeader.udp(request, ip)!!
        val query = DnsCodec.parseQuery(request, udp.payloadOffset, udp.payloadLength)!!

        val reply = PacketBuilder.udpReply(
            request, ip, udp, DnsCodec.nxDomainFor(request, udp.payloadOffset, query),
        )

        val replyIp = IpPacket.parse(reply)!!
        val replyUdp = TransportHeader.udp(reply, replyIp)!!
        val dns = reply.copyOfRange(replyUdp.payloadOffset, replyUdp.payloadOffset + replyUdp.payloadLength)

        assertEquals("the id the app is waiting for", 0x1234, u16(dns, 0))
        assertEquals("a response", 0x8000, u16(dns, 2) and 0x8000)
        assertEquals("name error", 3, u16(dns, 2) and 0x000F)
        assertIpv4HeaderChecksum(reply)
        assertTransportChecksum(reply, version = 4, protocol = IpHeader.PROTO_UDP, checksumAt = 20 + 6)
    }

    // ---- the reset --------------------------------------------------------------------------

    /**
     * RFC 793 §3.4: a segment carrying ACK is reset with `SEQ = SEG.ACK` and no ACK of its own.
     * A reset with the wrong sequence is discarded as out of window and the connection just hangs —
     * which is the failure a reset exists to avoid.
     */
    @Test
    fun `a reset of an established segment takes its sequence from the segment's ack`() {
        val request = ipv4Tcp(seq = 0x1000_0000L, ack = 0x2000_0000L, flags = TcpHeader.FLAG_ACK or TcpHeader.FLAG_PSH, payloadBytes = 517)
        val ip = IpPacket.parse(request)!!
        val tcp = TransportHeader.tcp(request, ip)!!

        val reset = PacketBuilder.tcpReset(request, ip, tcp)
        val resetTcp = TransportHeader.tcp(reset, IpPacket.parse(reset)!!)!!

        assertEquals(0x2000_0000L, resetTcp.sequenceNumber)
        assertTrue(resetTcp.isRst)
        assertEquals("no ACK of its own", false, resetTcp.isAck)
    }

    @Test
    fun `a reset of a SYN acknowledges the SYN it is refusing`() {
        val request = ipv4Tcp(seq = 0xFFFF_FFFFL, ack = 0, flags = TcpHeader.FLAG_SYN, payloadBytes = 0)
        val ip = IpPacket.parse(request)!!
        val tcp = TransportHeader.tcp(request, ip)!!

        val reset = PacketBuilder.tcpReset(request, ip, tcp)
        val resetTcp = TransportHeader.tcp(reset, IpPacket.parse(reset)!!)!!

        assertEquals(0L, resetTcp.sequenceNumber)
        assertEquals("SYN counts as one byte, and the sum wraps at 2^32", 0L, resetTcp.acknowledgementNumber)
        assertTrue(resetTcp.isRst)
        assertTrue(resetTcp.isAck)
    }

    @Test
    fun `the reset is addressed back at the app and its checksums are right`() {
        val request = ipv4Tcp(seq = 5, ack = 9, flags = TcpHeader.FLAG_ACK, payloadBytes = 40)
        val ip = IpPacket.parse(request)!!
        val tcp = TransportHeader.tcp(request, ip)!!

        val reset = PacketBuilder.tcpReset(request, ip, tcp)
        val resetTcp = TransportHeader.tcp(reset, IpPacket.parse(reset)!!)!!

        assertEquals(tcp.destinationPort, resetTcp.sourcePort)
        assertEquals(tcp.sourcePort, resetTcp.destinationPort)
        assertIpv4HeaderChecksum(reset)
        assertTransportChecksum(reset, version = 4, protocol = IpHeader.PROTO_TCP, checksumAt = 20 + 16)
    }

    @Test
    fun `an IPv6 reset is a whole IPv6 packet`() {
        val request = ipv6Tcp(seq = 5, ack = 9, flags = TcpHeader.FLAG_ACK, payloadBytes = 40)
        val ip = IpPacket.parse(request)!!
        val tcp = TransportHeader.tcp(request, ip)!!

        val reset = PacketBuilder.tcpReset(request, ip, tcp)

        assertEquals(60, reset.size)
        assertEquals(20, u16(reset, 4))
        assertEquals(IpHeader.PROTO_TCP, reset[6].toInt() and 0xFF)
        assertTransportChecksum(reset, version = 6, protocol = IpHeader.PROTO_TCP, checksumAt = 40 + 16)
    }

    // ---- the oracle -------------------------------------------------------------------------

    private fun assertIpv4HeaderChecksum(packet: ByteArray) {
        val header = packet.copyOfRange(0, 20)
        val written = u16(header, 10)
        header[10] = 0
        header[11] = 0

        assertEquals("IPv4 header checksum", onesComplement(header), written)
    }

    private fun assertTransportChecksum(packet: ByteArray, version: Int, protocol: Int, checksumAt: Int) {
        val headerBytes = if (version == 4) 20 else 40
        val addressAt = if (version == 4) 12 else 8
        val addressBytes = if (version == 4) 4 else 16
        val transport = packet.copyOfRange(headerBytes, packet.size)
        val written = u16(packet, checksumAt)
        transport[checksumAt - headerBytes] = 0
        transport[checksumAt - headerBytes + 1] = 0

        val pseudo = ByteArrayOutputStream()
        pseudo.write(packet, addressAt, addressBytes * 2)
        if (version == 4) {
            pseudo.write(byteArrayOf(0, protocol.toByte()))
            pseudo.write(byteArrayOf((transport.size shr 8).toByte(), transport.size.toByte()))
        } else {
            pseudo.write(byteArrayOf(0, 0, (transport.size shr 8).toByte(), transport.size.toByte()))
            pseudo.write(byteArrayOf(0, 0, 0, protocol.toByte()))
        }
        pseudo.write(transport)

        assertEquals("transport checksum", onesComplement(pseudo.toByteArray()), written)
    }

    private fun onesComplement(bytes: ByteArray): Int {
        var accumulator = 0
        var at = 0
        while (at + 1 < bytes.size) {
            accumulator += ((bytes[at].toInt() and 0xFF) shl 8) or (bytes[at + 1].toInt() and 0xFF)
            at += 2
        }
        if (at < bytes.size) accumulator += (bytes[at].toInt() and 0xFF) shl 8
        while (accumulator ushr 16 != 0) accumulator = (accumulator and 0xFFFF) + (accumulator ushr 16)
        return accumulator.inv() and 0xFFFF
    }

    // ---- fixtures ---------------------------------------------------------------------------

    private fun udpReplyFor(request: ByteArray, payload: ByteArray): ByteArray {
        val ip = IpPacket.parse(request)!!
        return PacketBuilder.udpReply(request, ip, TransportHeader.udp(request, ip)!!, payload)
    }

    private fun ipv4Udp(
        source: Int,
        destination: Int,
        sourceAddress: ByteArray = byteArrayOf(10, 0, 0, 2),
        payload: ByteArray = byteArrayOf(1, 2, 3, 4),
    ): ByteArray {
        val udp = ByteArrayOutputStream()
        udp.write(u16b(source))
        udp.write(u16b(destination))
        udp.write(u16b(8 + payload.size))
        udp.write(u16b(0))
        udp.write(payload)
        return ipv4(IpHeader.PROTO_UDP, udp.toByteArray(), sourceAddress)
    }

    private fun ipv6Udp(source: Int, destination: Int, payload: ByteArray = byteArrayOf(1, 2, 3, 4)): ByteArray {
        val udp = ByteArrayOutputStream()
        udp.write(u16b(source))
        udp.write(u16b(destination))
        udp.write(u16b(8 + payload.size))
        udp.write(u16b(0))
        udp.write(payload)
        return ipv6(IpHeader.PROTO_UDP, udp.toByteArray())
    }

    private fun ipv4Tcp(seq: Long, ack: Long, flags: Int, payloadBytes: Int) =
        ipv4(IpHeader.PROTO_TCP, tcpSegment(seq, ack, flags, payloadBytes), byteArrayOf(10, 0, 0, 2))

    private fun ipv6Tcp(seq: Long, ack: Long, flags: Int, payloadBytes: Int) =
        ipv6(IpHeader.PROTO_TCP, tcpSegment(seq, ack, flags, payloadBytes))

    private fun tcpSegment(seq: Long, ack: Long, flags: Int, payloadBytes: Int): ByteArray {
        val out = ByteArrayOutputStream()
        out.write(u16b(44100))
        out.write(u16b(443))
        out.write(u32b(seq))
        out.write(u32b(ack))
        out.write(5 shl 4)
        out.write(flags)
        out.write(u16b(64240))
        out.write(u16b(0))
        out.write(u16b(0))
        out.write(ByteArray(payloadBytes) { 0x16 })
        return out.toByteArray()
    }

    private fun ipv4(protocol: Int, payload: ByteArray, sourceAddress: ByteArray): ByteArray {
        val out = ByteArrayOutputStream()
        out.write(0x45)
        out.write(0)
        out.write(u16b(20 + payload.size))
        out.write(u16b(0x1234))
        out.write(u16b(0x4000))
        out.write(64)
        out.write(protocol)
        out.write(u16b(0))
        out.write(sourceAddress)
        out.write(byteArrayOf(1, 1, 1, 1))
        out.write(payload)
        return out.toByteArray()
    }

    private fun ipv6(nextHeader: Int, payload: ByteArray): ByteArray {
        val out = ByteArrayOutputStream()
        out.write(byteArrayOf(0x60, 0, 0, 0))
        out.write(u16b(payload.size))
        out.write(nextHeader)
        out.write(64)
        out.write(ByteArray(16) { if (it == 15) 2 else 0x20 })
        out.write(ByteArray(16) { if (it == 15) 1 else 0x20 })
        out.write(payload)
        return out.toByteArray()
    }

    private fun dnsQuery(name: String): ByteArray {
        val out = ByteArrayOutputStream()
        out.write(byteArrayOf(0x12, 0x34, 0x01, 0x00, 0, 1, 0, 0, 0, 0, 0, 0))
        for (label in name.split('.')) {
            out.write(label.length)
            out.write(label.toByteArray(Charsets.US_ASCII))
        }
        out.write(0)
        out.write(byteArrayOf(0, 1, 0, 1))
        return out.toByteArray()
    }

    private fun u16(b: ByteArray, at: Int) = ((b[at].toInt() and 0xFF) shl 8) or (b[at + 1].toInt() and 0xFF)

    private fun u16b(value: Int) = byteArrayOf((value shr 8).toByte(), value.toByte())

    private fun u32b(value: Long) = byteArrayOf(
        (value shr 24).toByte(), (value shr 16).toByte(), (value shr 8).toByte(), value.toByte(),
    )
}
