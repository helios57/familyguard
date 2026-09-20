package io.github.helios57.familyguard.filter

/**
 * Writing packets back into the tunnel: a DNS answer the filter made up, and a reset that ends a
 * blocked connection.
 *
 * Both are *injected*, not forwarded — they are written to the TUN file descriptor so the app's own
 * network stack receives them as if they had come off the network. That is the only way to answer
 * an app from inside a `VpnService`, and it means every field the kernel will check has to be right
 * here, including the checksums nobody sees until they are wrong.
 *
 * The checksums are the part worth reading twice. A wrong one is not an error: the app's stack
 * silently discards the packet, the DNS query goes unanswered, the app retries, and the symptom is
 * *"the game takes ten seconds to load"* rather than *"the filter is broken"*.
 */
object PacketBuilder {

    private const val DEFAULT_TTL = 64

    /**
     * An IP+UDP packet carrying [payload] back to whoever sent [udp].
     *
     * Addresses and ports are swapped from the request, so this arrives at the app as the answer to
     * the query it just made.
     */
    fun udpReply(request: ByteArray, ip: IpHeader, udp: UdpHeader, payload: ByteArray): ByteArray {
        val addressBytes = if (ip.version == 4) 4 else 16
        return udpDatagram(
            version = ip.version,
            sourceAddress = request.copyOfRange(ip.destinationOffset, ip.destinationOffset + addressBytes),
            destinationAddress = request.copyOfRange(ip.sourceOffset, ip.sourceOffset + addressBytes),
            sourcePort = udp.destinationPort,
            destinationPort = udp.sourcePort,
            payload = payload,
        )
    }

    /**
     * An IP+UDP datagram this filter originates, with the four-tuple stated rather than reflected.
     *
     * The relay half needs this: a reply arriving on a socket minutes after the query has no
     * request packet left to swap addresses out of, and keeping the original bytes alive just to
     * reverse them would be holding a whole packet to recover twelve of its fields.
     */
    fun udpDatagram(
        version: Int,
        sourceAddress: ByteArray,
        destinationAddress: ByteArray,
        sourcePort: Int,
        destinationPort: Int,
        payload: ByteArray,
        payloadOffset: Int = 0,
        payloadLength: Int = payload.size - payloadOffset,
    ): ByteArray {
        val headerBytes = if (version == 4) 20 else 40
        val out = ByteArray(headerBytes + 8 + payloadLength)
        writeIpHeaderFrom(out, version, sourceAddress, destinationAddress, IpHeader.PROTO_UDP, 8 + payloadLength)

        val udpAt = headerBytes
        put16(out, udpAt, sourcePort)
        put16(out, udpAt + 2, destinationPort)
        put16(out, udpAt + 4, 8 + payloadLength)
        put16(out, udpAt + 6, 0)
        System.arraycopy(payload, payloadOffset, out, udpAt + 8, payloadLength)

        // The pseudo-header uses the addresses of the packet being SENT, so they are read back out
        // of `out` and never out of whatever this is answering.
        val sum = transportChecksum(out, version, IpHeader.PROTO_UDP, udpAt, 8 + payloadLength)
        // Zero means "no checksum" in IPv4 UDP and is illegal in IPv6, so it is sent as its other
        // ones-complement spelling instead.
        put16(out, udpAt + 6, if (sum == 0) 0xFFFF else sum)
        return out
    }

    /**
     * A TCP reset ending the connection [tcp] belongs to, addressed back at the app that opened it.
     *
     * RST rather than dropping the segment, because a drop is indistinguishable from a bad network:
     * the app retries with backoff, holds the socket open, and the child watches a spinner. A reset
     * is an immediate, ordinary failure that every HTTP client already handles — which is exactly
     * what a blocked ad request should look like from inside the app.
     *
     * The sequence numbers follow RFC 793 §3.4: a segment carrying ACK is reset with
     * `SEQ = SEG.ACK` and no ACK of its own, and anything else with `SEQ = 0` and
     * `ACK = SEG.SEQ + SEG.LEN`. Getting this wrong produces a reset the peer's stack discards as
     * out of window, and then the connection simply hangs — the failure this is meant to prevent.
     */
    fun tcpReset(request: ByteArray, ip: IpHeader, tcp: TcpHeader): ByteArray {
        val headerBytes = if (ip.version == 4) 20 else 40
        val out = ByteArray(headerBytes + 20)
        writeIpHeader(out, request, ip, IpHeader.PROTO_TCP, 20)

        val segmentLength = tcp.payloadLength.toLong() +
            (if (tcp.isSyn) 1 else 0) + (if (tcp.isFin) 1 else 0)

        val sequence: Long
        val acknowledgement: Long
        val flags: Int
        if (tcp.isAck) {
            sequence = tcp.acknowledgementNumber
            acknowledgement = 0
            flags = TcpHeader.FLAG_RST
        } else {
            sequence = 0
            acknowledgement = (tcp.sequenceNumber + segmentLength) and 0xFFFFFFFFL
            flags = TcpHeader.FLAG_RST or TcpHeader.FLAG_ACK
        }

        val tcpAt = headerBytes
        put16(out, tcpAt, tcp.destinationPort)
        put16(out, tcpAt + 2, tcp.sourcePort)
        put32(out, tcpAt + 4, sequence)
        put32(out, tcpAt + 8, acknowledgement)
        out[tcpAt + 12] = (5 shl 4).toByte() // data offset 5 words, no options
        out[tcpAt + 13] = flags.toByte()
        put16(out, tcpAt + 14, 0) // window — a reset closes the connection, so it needs none
        put16(out, tcpAt + 16, 0) // checksum, filled in below
        put16(out, tcpAt + 18, 0) // urgent pointer

        put16(out, tcpAt + 16, transportChecksum(out, ip.version, IpHeader.PROTO_TCP, tcpAt, 20))
        return out
    }

    /**
     * An IP+TCP segment this filter ORIGINATES, rather than one it reflects.
     *
     * [tcpReset] answers a packet that just arrived and can take every address and port from it.
     * A flow machine cannot: it speaks for a server the app is trying to reach, and most of what it
     * sends — the SYN-ACK, a bare ACK, the server's own data on its way back — answers nothing that
     * arrived in that instant. So the four-tuple is passed in, and it is stated from the point of
     * view of the packet being written: [sourceAddress]/[sourcePort] is the peer the app thinks it
     * is talking to, and the packet is addressed to the app.
     */
    fun tcpSegment(
        version: Int,
        sourceAddress: ByteArray,
        destinationAddress: ByteArray,
        sourcePort: Int,
        destinationPort: Int,
        sequence: Long,
        acknowledgement: Long,
        flags: Int,
        window: Int,
        payload: ByteArray = EMPTY,
        payloadOffset: Int = 0,
        payloadLength: Int = payload.size - payloadOffset,
    ): ByteArray {
        val headerBytes = if (version == 4) 20 else 40
        val out = ByteArray(headerBytes + 20 + payloadLength)
        writeIpHeaderFrom(out, version, sourceAddress, destinationAddress, IpHeader.PROTO_TCP, 20 + payloadLength)

        val tcpAt = headerBytes
        put16(out, tcpAt, sourcePort)
        put16(out, tcpAt + 2, destinationPort)
        put32(out, tcpAt + 4, sequence and 0xFFFFFFFFL)
        put32(out, tcpAt + 8, acknowledgement and 0xFFFFFFFFL)
        out[tcpAt + 12] = (5 shl 4).toByte()
        out[tcpAt + 13] = flags.toByte()
        // Clamped rather than truncated. A window of 70000 written into 16 bits becomes 4464 with
        // no error anywhere, and the connection then runs at a fraction of its speed for reasons
        // nothing reports.
        put16(out, tcpAt + 14, window.coerceIn(0, 0xFFFF))
        put16(out, tcpAt + 16, 0)
        put16(out, tcpAt + 18, 0)
        if (payloadLength > 0) System.arraycopy(payload, payloadOffset, out, tcpAt + 20, payloadLength)

        put16(out, tcpAt + 16, transportChecksum(out, version, IpHeader.PROTO_TCP, tcpAt, 20 + payloadLength))
        return out
    }

    private val EMPTY = ByteArray(0)

    /** The IP header of a packet going back the way [ip] came, with the addresses swapped. */
    private fun writeIpHeader(
        out: ByteArray,
        request: ByteArray,
        ip: IpHeader,
        protocol: Int,
        payloadLength: Int,
    ) {
        val addressBytes = if (ip.version == 4) 4 else 16
        writeIpHeaderFrom(
            out,
            ip.version,
            request.copyOfRange(ip.destinationOffset, ip.destinationOffset + addressBytes),
            request.copyOfRange(ip.sourceOffset, ip.sourceOffset + addressBytes),
            protocol,
            payloadLength,
        )
    }

    private fun writeIpHeaderFrom(
        out: ByteArray,
        version: Int,
        sourceAddress: ByteArray,
        destinationAddress: ByteArray,
        protocol: Int,
        payloadLength: Int,
    ) {
        if (version == 4) {
            out[0] = 0x45 // version 4, header length 5 words
            out[1] = 0
            put16(out, 2, 20 + payloadLength)
            put16(out, 4, 0) // identification — only meaningful for fragments, and these never are
            put16(out, 6, 0x4000) // don't fragment
            out[8] = DEFAULT_TTL.toByte()
            out[9] = protocol.toByte()
            put16(out, 10, 0)
            System.arraycopy(sourceAddress, 0, out, 12, 4)
            System.arraycopy(destinationAddress, 0, out, 16, 4)
            put16(out, 10, checksum(out, 0, 20))
        } else {
            out[0] = 0x60 // version 6
            out[1] = 0
            out[2] = 0
            out[3] = 0
            put16(out, 4, payloadLength)
            out[6] = protocol.toByte()
            out[7] = DEFAULT_TTL.toByte()
            System.arraycopy(sourceAddress, 0, out, 8, 16)
            System.arraycopy(destinationAddress, 0, out, 24, 16)
        }
    }

    /**
     * The transport checksum over the pseudo-header and [length] bytes starting at [transportAt].
     *
     * The pseudo-header is the part that is easy to skip and impossible to notice missing in a unit
     * test that only checks the arithmetic: it covers the source and destination addresses, so a
     * checksum computed without it is correct about the payload and wrong about who the packet is
     * for.
     */
    private fun transportChecksum(packet: ByteArray, version: Int, protocol: Int, transportAt: Int, length: Int): Int {
        val addressAt = if (version == 4) 12 else 8
        val addressBytes = if (version == 4) 4 else 16

        var sum = 0L
        var at = addressAt
        val addressEnd = addressAt + addressBytes * 2
        while (at < addressEnd) {
            sum += ((packet[at].toInt() and 0xFF) shl 8) or (packet[at + 1].toInt() and 0xFF)
            at += 2
        }
        sum += protocol.toLong()
        sum += length.toLong()
        sum += sumOf(packet, transportAt, length)
        return fold(sum)
    }

    private fun checksum(packet: ByteArray, from: Int, length: Int): Int = fold(sumOf(packet, from, length))

    private fun sumOf(packet: ByteArray, from: Int, length: Int): Long {
        var sum = 0L
        var at = from
        val end = from + length
        while (at + 1 < end) {
            sum += ((packet[at].toInt() and 0xFF) shl 8) or (packet[at + 1].toInt() and 0xFF)
            at += 2
        }
        // An odd trailing byte is padded on the right, not the left. Padding it the other way
        // produces a checksum that is wrong only for odd-length payloads, which is most DNS names.
        if (at < end) sum += (packet[at].toInt() and 0xFF) shl 8
        return sum
    }

    private fun fold(value: Long): Int {
        var sum = value
        while (sum shr 16 != 0L) sum = (sum and 0xFFFF) + (sum shr 16)
        return (sum.inv() and 0xFFFF).toInt()
    }

    private fun put16(b: ByteArray, at: Int, value: Int) {
        b[at] = ((value shr 8) and 0xFF).toByte()
        b[at + 1] = (value and 0xFF).toByte()
    }

    private fun put32(b: ByteArray, at: Int, value: Long) {
        b[at] = ((value shr 24) and 0xFF).toByte()
        b[at + 1] = ((value shr 16) and 0xFF).toByte()
        b[at + 2] = ((value shr 8) and 0xFF).toByte()
        b[at + 3] = (value and 0xFF).toByte()
    }
}
