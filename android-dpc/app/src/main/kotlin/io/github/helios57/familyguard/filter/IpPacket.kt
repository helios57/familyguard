package io.github.helios57.familyguard.filter

/**
 * Where the interesting parts of an IP packet are, without copying any of it.
 *
 * Every field is an absolute offset into the caller's buffer. The tunnel reads one packet per
 * iteration into a reused array, so a parser that allocated per packet would allocate per packet
 * forever — on the one thread that must never pause.
 */
data class IpHeader(
    /** 4 or 6. */
    val version: Int,
    /** The transport protocol, after any IPv6 extension headers: [PROTO_TCP], [PROTO_UDP], … */
    val protocol: Int,
    val sourceOffset: Int,
    val destinationOffset: Int,
    /** 4 for IPv4, 16 for IPv6 — the width of both address fields. */
    val addressBytes: Int,
    val payloadOffset: Int,
    val payloadLength: Int,
) {
    companion object {
        const val PROTO_ICMP = 1
        const val PROTO_TCP = 6
        const val PROTO_UDP = 17
        const val PROTO_ICMPV6 = 58
    }
}

/**
 * Reading IP headers off the tunnel.
 *
 * **Every refusal here means "forward this packet untouched".** That is the whole safety posture of
 * the filter in one sentence: a packet this code cannot understand is a packet it has no opinion
 * about, and the alternative — dropping what it cannot parse — turns every parser gap into a broken
 * app with no error message anywhere.
 *
 * Two refusals are worth naming, because both are cases where the obvious code produces confident
 * nonsense rather than an error:
 *
 *  - **A non-first fragment has no transport header in it.** The bytes at the payload offset are
 *    the middle of someone's data, and read as a TCP header they yield plausible port numbers.
 *    Refused on a non-zero fragment offset, in both IPv4 and the IPv6 fragment extension header.
 *  - **A truncated read.** `totalLength` is what the sender claims; the buffer is what arrived. When
 *    the claim exceeds the bytes on hand the packet is refused rather than parsed to the shorter of
 *    the two, because every length downstream would then be a guess.
 */
object IpPacket {

    /** Hop-by-hop, routing, fragment, destination options, mobility — the ones that chain. */
    private val EXTENSION_HEADERS = setOf(0, 43, 44, 60, 135)
    private const val MAX_EXTENSION_HEADERS = 8

    fun parse(buffer: ByteArray, offset: Int = 0, length: Int = buffer.size - offset): IpHeader? {
        if (offset < 0 || length < 1 || offset + length > buffer.size) return null
        return when ((buffer[offset].toInt() and 0xF0) ushr 4) {
            4 -> parseV4(buffer, offset, length)
            6 -> parseV6(buffer, offset, length)
            else -> null
        }
    }

    private fun parseV4(buffer: ByteArray, offset: Int, length: Int): IpHeader? {
        if (length < 20) return null
        val headerLength = (buffer[offset].toInt() and 0x0F) * 4
        if (headerLength < 20 || headerLength > length) return null

        val totalLength = u16(buffer, offset + 2)
        if (totalLength < headerLength || totalLength > length) return null

        // Bits 0..12 of the flags/offset word are the fragment offset, in 8-byte units.
        if (u16(buffer, offset + 6) and 0x1FFF != 0) return null

        return IpHeader(
            version = 4,
            protocol = buffer[offset + 9].toInt() and 0xFF,
            sourceOffset = offset + 12,
            destinationOffset = offset + 16,
            addressBytes = 4,
            payloadOffset = offset + headerLength,
            payloadLength = totalLength - headerLength,
        )
    }

    private fun parseV6(buffer: ByteArray, offset: Int, length: Int): IpHeader? {
        if (length < 40) return null
        val payloadLength = u16(buffer, offset + 4)
        if (40 + payloadLength > length) return null

        var next = buffer[offset + 6].toInt() and 0xFF
        var at = offset + 40
        val end = offset + 40 + payloadLength
        var hops = 0
        while (next in EXTENSION_HEADERS) {
            if (++hops > MAX_EXTENSION_HEADERS) return null
            if (at + 8 > end) return null
            if (next == 44) {
                // Fragment header: 13-bit offset in the top of the second 16-bit word.
                if (u16(buffer, at + 2) and 0xFFF8 != 0) return null
                next = buffer[at].toInt() and 0xFF
                at += 8
            } else {
                // Hdr Ext Len counts 8-byte units *not including* the first eight bytes.
                val extensionLength = ((buffer[at + 1].toInt() and 0xFF) + 1) * 8
                next = buffer[at].toInt() and 0xFF
                at += extensionLength
                if (at > end) return null
            }
        }

        return IpHeader(
            version = 6,
            protocol = next,
            sourceOffset = offset + 8,
            destinationOffset = offset + 24,
            addressBytes = 16,
            payloadOffset = at,
            payloadLength = end - at,
        )
    }

    private fun u16(b: ByteArray, at: Int): Int =
        ((b[at].toInt() and 0xFF) shl 8) or (b[at + 1].toInt() and 0xFF)
}

/** A UDP datagram's ports and where its payload starts. */
data class UdpHeader(
    val sourcePort: Int,
    val destinationPort: Int,
    val payloadOffset: Int,
    val payloadLength: Int,
)

/** A TCP segment's ports, sequence numbers, flags, and where its payload starts. */
data class TcpHeader(
    val sourcePort: Int,
    val destinationPort: Int,
    /** Unsigned on the wire; kept as [Long] so arithmetic on it does not silently go negative. */
    val sequenceNumber: Long,
    val acknowledgementNumber: Long,
    val flags: Int,
    val windowSize: Int,
    val payloadOffset: Int,
    val payloadLength: Int,
) {
    val isSyn: Boolean get() = flags and FLAG_SYN != 0
    val isAck: Boolean get() = flags and FLAG_ACK != 0
    val isFin: Boolean get() = flags and FLAG_FIN != 0
    val isRst: Boolean get() = flags and FLAG_RST != 0

    companion object {
        const val FLAG_FIN = 0x01
        const val FLAG_SYN = 0x02
        const val FLAG_RST = 0x04
        const val FLAG_PSH = 0x08
        const val FLAG_ACK = 0x10
    }
}

/** Reading the transport header that [IpPacket] located. Same posture: null means forward. */
object TransportHeader {

    fun udp(buffer: ByteArray, ip: IpHeader): UdpHeader? {
        if (ip.protocol != IpHeader.PROTO_UDP || ip.payloadLength < 8) return null
        val declared = u16(buffer, ip.payloadOffset + 4)
        // The UDP length covers its own header, so anything under 8 is malformed, and a claim
        // longer than the IP payload is a claim about bytes that are not here.
        if (declared < 8 || declared > ip.payloadLength) return null
        return UdpHeader(
            sourcePort = u16(buffer, ip.payloadOffset),
            destinationPort = u16(buffer, ip.payloadOffset + 2),
            payloadOffset = ip.payloadOffset + 8,
            payloadLength = declared - 8,
        )
    }

    fun tcp(buffer: ByteArray, ip: IpHeader): TcpHeader? {
        if (ip.protocol != IpHeader.PROTO_TCP || ip.payloadLength < 20) return null
        val dataOffset = ((buffer[ip.payloadOffset + 12].toInt() and 0xF0) ushr 4) * 4
        if (dataOffset < 20 || dataOffset > ip.payloadLength) return null
        return TcpHeader(
            sourcePort = u16(buffer, ip.payloadOffset),
            destinationPort = u16(buffer, ip.payloadOffset + 2),
            sequenceNumber = u32(buffer, ip.payloadOffset + 4),
            acknowledgementNumber = u32(buffer, ip.payloadOffset + 8),
            flags = buffer[ip.payloadOffset + 13].toInt() and 0xFF,
            windowSize = u16(buffer, ip.payloadOffset + 14),
            payloadOffset = ip.payloadOffset + dataOffset,
            payloadLength = ip.payloadLength - dataOffset,
        )
    }

    private fun u16(b: ByteArray, at: Int): Int =
        ((b[at].toInt() and 0xFF) shl 8) or (b[at + 1].toInt() and 0xFF)

    private fun u32(b: ByteArray, at: Int): Long =
        ((b[at].toLong() and 0xFF) shl 24) or ((b[at + 1].toLong() and 0xFF) shl 16) or
            ((b[at + 2].toLong() and 0xFF) shl 8) or (b[at + 3].toLong() and 0xFF)
}
