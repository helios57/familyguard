package io.github.helios57.familyguard.filter

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Test
import java.io.ByteArrayOutputStream

/**
 * Finding the transport header in a packet off the tunnel.
 *
 * Every refusal in here means *forward the packet untouched*, so the cases that matter are the ones
 * where the obvious code does not fail — it succeeds, confidently, on nonsense. A non-first
 * fragment read as TCP yields perfectly plausible port numbers out of the middle of someone's
 * payload, and nothing downstream can tell that it did.
 */
class IpPacketTest {

    @Test
    fun `an IPv4 UDP datagram gives up its ports and payload`() {
        val packet = ipv4(IpHeader.PROTO_UDP, udp(53000, 53, DNS_BYTES))

        val ip = IpPacket.parse(packet)!!
        val u = TransportHeader.udp(packet, ip)!!

        assertEquals(4, ip.version)
        assertEquals(53000, u.sourcePort)
        assertEquals(53, u.destinationPort)
        assertEquals(DNS_BYTES.size, u.payloadLength)
        assertEquals(0x12, packet[u.payloadOffset].toInt())
    }

    @Test
    fun `an IPv6 UDP datagram gives up its ports and payload`() {
        val packet = ipv6(IpHeader.PROTO_UDP, udp(53000, 53, DNS_BYTES))

        val ip = IpPacket.parse(packet)!!
        val u = TransportHeader.udp(packet, ip)!!

        assertEquals(6, ip.version)
        assertEquals(16, ip.addressBytes)
        assertEquals(53, u.destinationPort)
        assertEquals(DNS_BYTES.size, u.payloadLength)
    }

    /** Android hands out IPv6 with extension headers on real carriers; UDP is behind the chain. */
    @Test
    fun `an extension header chain is walked to the transport`() {
        val hopByHop = byteArrayOf(60, 0) + ByteArray(6)            // next = destination options
        val destinationOptions = byteArrayOf(IpHeader.PROTO_UDP.toByte(), 0) + ByteArray(6)
        val packet = ipv6(0, hopByHop + destinationOptions + udp(53000, 53, DNS_BYTES))

        val ip = IpPacket.parse(packet)!!

        assertEquals(IpHeader.PROTO_UDP, ip.protocol)
        assertEquals(53, TransportHeader.udp(packet, ip)!!.destinationPort)
    }

    @Test
    fun `an extension header chain that never ends is refused rather than walked forever`() {
        val link = byteArrayOf(60, 0) + ByteArray(6) // destination options pointing at itself
        var chain = ByteArray(0)
        repeat(12) { chain += link }

        assertNull(IpPacket.parse(ipv6(60, chain + udp(1, 2, DNS_BYTES))))
    }

    /**
     * The bytes at a non-first fragment's payload offset are the middle of someone's data. Read as
     * a TCP header they produce ports, flags and a sequence number, all of them fiction.
     */
    @Test
    fun `a non-first fragment is refused, not parsed as a transport header`() {
        assertNull(IpPacket.parse(ipv4(IpHeader.PROTO_UDP, udp(53000, 53, DNS_BYTES), fragmentOffset = 185)))
    }

    /** The first fragment does carry the transport header, so it is parsed. */
    @Test
    fun `the first fragment of a fragmented packet is still parsed`() {
        val packet = ipv4(IpHeader.PROTO_UDP, udp(53000, 53, DNS_BYTES), moreFragments = true)

        assertEquals(53, TransportHeader.udp(packet, IpPacket.parse(packet)!!)!!.destinationPort)
    }

    @Test
    fun `an IPv6 fragment that is not the first is refused`() {
        // Fragment header: next=UDP, reserved, offset 0x00B8 in the top 13 bits, identification.
        val fragment = byteArrayOf(IpHeader.PROTO_UDP.toByte(), 0, 0x05, 0xC0.toByte(), 0, 0, 0, 1)

        assertNull(IpPacket.parse(ipv6(44, fragment + udp(1, 2, DNS_BYTES))))
    }

    @Test
    fun `an IPv6 fragment header at offset zero is parsed`() {
        val fragment = byteArrayOf(IpHeader.PROTO_UDP.toByte(), 0, 0, 1, 0, 0, 0, 1)
        val packet = ipv6(44, fragment + udp(53000, 53, DNS_BYTES))

        assertEquals(53, TransportHeader.udp(packet, IpPacket.parse(packet)!!)!!.destinationPort)
    }

    /**
     * Every truncation. `totalLength` is the sender's claim and the buffer is what arrived; when
     * they disagree the packet is refused rather than parsed to the shorter of the two, because
     * every length downstream would then be a guess dressed as a measurement.
     */
    @Test
    fun `no prefix of a packet parses`() {
        for (packet in listOf(ipv4(IpHeader.PROTO_UDP, udp(1, 53, DNS_BYTES)), ipv6(IpHeader.PROTO_UDP, udp(1, 53, DNS_BYTES)))) {
            for (length in 0 until packet.size) {
                assertNull("a $length-byte prefix of ${packet.size}", IpPacket.parse(packet, 0, length))
            }
            assertNotNull(IpPacket.parse(packet))
        }
    }

    @Test
    fun `a header length below the minimum is refused`() {
        val packet = ipv4(IpHeader.PROTO_UDP, udp(1, 53, DNS_BYTES))
        packet[0] = 0x44 // IHL 4 words, which cannot hold an IPv4 header

        assertNull(IpPacket.parse(packet))
    }

    @Test
    fun `neither version 4 nor 6 is refused`() {
        val packet = ipv4(IpHeader.PROTO_UDP, udp(1, 53, DNS_BYTES))
        packet[0] = 0x55

        assertNull(IpPacket.parse(packet))
    }

    @Test
    fun `a UDP length claiming more than the IP payload holds is refused`() {
        val packet = ipv4(IpHeader.PROTO_UDP, udp(1, 53, DNS_BYTES, declaredLength = 4000))

        assertNull(TransportHeader.udp(packet, IpPacket.parse(packet)!!))
    }

    @Test
    fun `a TCP segment gives up its flags, sequence and payload`() {
        val payload = byteArrayOf(0x16, 3, 1, 0, 5)
        val packet = ipv4(IpHeader.PROTO_TCP, tcp(44100, 443, seq = 0xF000_0001L, ack = 7L, flags = 0x18, payload = payload))

        val tcpHeader = TransportHeader.tcp(packet, IpPacket.parse(packet)!!)!!

        assertEquals(443, tcpHeader.destinationPort)
        assertEquals("unsigned, so a high sequence number does not go negative", 0xF000_0001L, tcpHeader.sequenceNumber)
        assertEquals(7L, tcpHeader.acknowledgementNumber)
        assertEquals(true, tcpHeader.isAck)
        assertEquals(false, tcpHeader.isSyn)
        assertEquals(payload.size, tcpHeader.payloadLength)
        assertEquals(0x16, packet[tcpHeader.payloadOffset].toInt())
    }

    /** Timestamps and SACK-permitted are on every real SYN, and they move the payload. */
    @Test
    fun `TCP options shift the payload offset`() {
        val payload = byteArrayOf(9, 9, 9)
        val packet = ipv4(
            IpHeader.PROTO_TCP,
            tcp(44100, 443, seq = 1, ack = 1, flags = 0x18, payload = payload, optionBytes = 12),
        )

        val tcpHeader = TransportHeader.tcp(packet, IpPacket.parse(packet)!!)!!

        assertEquals(payload.size, tcpHeader.payloadLength)
        assertEquals(9, packet[tcpHeader.payloadOffset].toInt())
    }

    @Test
    fun `a TCP data offset that runs past the segment is refused`() {
        val packet = ipv4(IpHeader.PROTO_TCP, tcp(1, 443, 1, 1, 0x10, byteArrayOf(1)))
        val ip = IpPacket.parse(packet)!!
        packet[ip.payloadOffset + 12] = (15 shl 4).toByte() // 60-byte header in a 21-byte segment

        assertNull(TransportHeader.tcp(packet, ip))
    }

    @Test
    fun `asking for UDP on a TCP packet gets nothing`() {
        val packet = ipv4(IpHeader.PROTO_TCP, tcp(1, 443, 1, 1, 0x10, byteArrayOf(1)))

        assertNull(TransportHeader.udp(packet, IpPacket.parse(packet)!!))
    }

    // ---- fixtures ---------------------------------------------------------------------------

    private fun ipv4(
        protocol: Int,
        payload: ByteArray,
        fragmentOffset: Int = 0,
        moreFragments: Boolean = false,
    ): ByteArray {
        val out = ByteArrayOutputStream()
        out.write(0x45)
        out.write(0)
        out.write(u16(20 + payload.size))
        out.write(u16(0x1234))
        out.write(u16((if (moreFragments) 0x2000 else 0) or fragmentOffset))
        out.write(64)
        out.write(protocol)
        out.write(u16(0))
        out.write(byteArrayOf(10, 0, 0, 2))
        out.write(byteArrayOf(1, 1, 1, 1))
        out.write(payload)
        return out.toByteArray()
    }

    private fun ipv6(nextHeader: Int, payload: ByteArray): ByteArray {
        val out = ByteArrayOutputStream()
        out.write(byteArrayOf(0x60, 0, 0, 0))
        out.write(u16(payload.size))
        out.write(nextHeader)
        out.write(64)
        out.write(ByteArray(16) { if (it == 15) 2 else 0x20 })
        out.write(ByteArray(16) { if (it == 15) 1 else 0x20 })
        out.write(payload)
        return out.toByteArray()
    }

    private fun udp(source: Int, destination: Int, payload: ByteArray, declaredLength: Int = -1): ByteArray {
        val out = ByteArrayOutputStream()
        out.write(u16(source))
        out.write(u16(destination))
        out.write(u16(if (declaredLength >= 0) declaredLength else 8 + payload.size))
        out.write(u16(0))
        out.write(payload)
        return out.toByteArray()
    }

    private fun tcp(
        source: Int,
        destination: Int,
        seq: Long,
        ack: Long,
        flags: Int,
        payload: ByteArray,
        optionBytes: Int = 0,
    ): ByteArray {
        val out = ByteArrayOutputStream()
        out.write(u16(source))
        out.write(u16(destination))
        out.write(u32(seq))
        out.write(u32(ack))
        out.write(((20 + optionBytes) / 4) shl 4)
        out.write(flags)
        out.write(u16(64240))
        out.write(u16(0))
        out.write(u16(0))
        out.write(ByteArray(optionBytes) { 1 }) // NOP padding
        out.write(payload)
        return out.toByteArray()
    }

    private fun u16(value: Int) = byteArrayOf((value shr 8).toByte(), value.toByte())

    private fun u32(value: Long) = byteArrayOf(
        (value shr 24).toByte(), (value shr 16).toByte(), (value shr 8).toByte(), value.toByte(),
    )

    private companion object {
        val DNS_BYTES = byteArrayOf(0x12, 0x34, 0x01, 0x00, 0, 1, 0, 0, 0, 0, 0, 0, 3, 0x61, 0x64, 0x73, 0)
    }
}
