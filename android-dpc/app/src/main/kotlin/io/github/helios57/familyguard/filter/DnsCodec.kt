package io.github.helios57.familyguard.filter

/**
 * The question a DNS query asked, plus the two header facts needed to answer it in its own terms.
 *
 * `name` is already lowercased with no trailing dot — the shape [DomainIndex] matches — so a caller
 * never has to remember to normalise before the lookup.
 */
data class DnsQuery(
    /** Echoed into the synthesised answer. A resolver that gets a different id discards the reply. */
    val transactionId: Int,
    val name: String,
    val type: Int,
    val recordClass: Int,
    /** Offset one past the question section, relative to the start of the message. */
    val questionEnd: Int,
)

/**
 * Reading the question out of a DNS query, and writing an NXDOMAIN back.
 *
 * **Nothing here throws.** Every read is bounds-checked and every refusal is a `null`, because this
 * runs once per packet on the tunnel's only thread: an exception on a malformed query is not a bug
 * report, it is the filter dying on the first hostile or merely unusual packet on the network.
 *
 * Two deliberate refusals, both of which mean *pass the packet through untouched*:
 *
 *  - **`QDCOUNT != 1`.** A multi-question query cannot be answered NXDOMAIN as a whole — the code is
 *    per-message, not per-question — so a filter that answered one would be lying about the others.
 *    In practice no resolver sends them; the point is that the refusal is explicit rather than an
 *    array index that happens to read the first question and ignore the rest.
 *  - **A compression pointer inside the question.** The question is the first name in the message,
 *    so there is nothing behind it to point at: a pointer there is malformed, and following one is
 *    how a name parser ends up in a loop. It is refused rather than followed.
 *
 * This is the UDP wire format. DNS over TCP prefixes a two-byte length, and DoT/DoH are not this
 * shape at all — they are handled a layer up by the name in the TLS ClientHello, not here.
 */
object DnsCodec {

    const val TYPE_A = 1
    const val TYPE_AAAA = 28
    /** RFC 9460. Chrome asks for this first, and a filter that ignores it leaks the connection. */
    const val TYPE_HTTPS = 65
    const val CLASS_IN = 1

    private const val HEADER_BYTES = 12
    private const val MAX_NAME_BYTES = 255
    private const val MAX_LABEL_BYTES = 63

    /** The question in [packet], or null if this is not a single-question query this can answer. */
    fun parseQuery(packet: ByteArray, offset: Int = 0, length: Int = packet.size - offset): DnsQuery? {
        if (offset < 0 || length < HEADER_BYTES || offset + length > packet.size) return null

        val flags = u16(packet, offset + 2)
        // QR set means this is a response; OPCODE other than QUERY (0) is not a name lookup.
        if (flags and 0x8000 != 0) return null
        if (flags and 0x7800 != 0) return null
        if (u16(packet, offset + 4) != 1) return null

        val name = StringBuilder()
        var at = offset + HEADER_BYTES
        val end = offset + length
        var nameBytes = 0
        while (true) {
            if (at >= end) return null
            val len = packet[at].toInt() and 0xFF
            if (len == 0) {
                at++
                break
            }
            // Top two bits set is a compression pointer; anything else with them set is reserved.
            if (len and 0xC0 != 0) return null
            if (len > MAX_LABEL_BYTES) return null
            nameBytes += len + 1
            if (nameBytes > MAX_NAME_BYTES) return null
            at++
            if (at + len > end) return null
            if (name.isNotEmpty()) name.append('.')
            for (i in 0 until len) {
                val b = packet[at + i].toInt() and 0xFF
                // A label byte can legally be anything, but a name that is not printable ASCII is
                // not a name this filter has a rule for, and letting it reach a log is an injection.
                if (b < 0x21 || b > 0x7E || b.toChar() == '.') return null
                name.append(b.toChar().lowercaseChar())
            }
            at += len
        }
        if (name.isEmpty()) return null
        if (at + 4 > end) return null

        val type = u16(packet, at)
        val recordClass = u16(packet, at + 2)
        return DnsQuery(
            transactionId = u16(packet, offset),
            name = name.toString(),
            type = type,
            recordClass = recordClass,
            questionEnd = (at + 4) - offset,
        )
    }

    /**
     * An NXDOMAIN answer to [query], to be sent back to the app that asked.
     *
     * NXDOMAIN rather than an `A` record pointing at `0.0.0.0`, because a synthesised address is a
     * route: the app opens a socket to it, the connection hangs for the platform's full timeout, and
     * the game sits on a black screen waiting for an ad that will never load. NXDOMAIN fails the
     * lookup immediately and every ad SDK already has a code path for a name that does not resolve.
     *
     * The answer carries no OPT record even if the query did. Dropping EDNS0 from a *failure* is
     * legal and costs nothing here — there is no payload to size and no extended code to report.
     */
    fun nxDomainFor(packet: ByteArray, offset: Int, query: DnsQuery): ByteArray {
        val out = ByteArray(query.questionEnd)
        System.arraycopy(packet, offset, out, 0, query.questionEnd)

        val flagsIn = u16(packet, offset + 2)
        // QR=1, OPCODE copied, AA=0, TC=0, RD copied, RA=1, Z=0, RCODE=3 (name error).
        val flagsOut = 0x8000 or (flagsIn and 0x7800) or (flagsIn and 0x0100) or 0x0080 or 0x0003
        put16(out, 2, flagsOut)
        put16(out, 4, 1) // QDCOUNT — the question is echoed back
        put16(out, 6, 0) // ANCOUNT
        put16(out, 8, 0) // NSCOUNT
        put16(out, 10, 0) // ARCOUNT
        return out
    }

    private fun u16(b: ByteArray, at: Int): Int =
        ((b[at].toInt() and 0xFF) shl 8) or (b[at + 1].toInt() and 0xFF)

    private fun put16(b: ByteArray, at: Int, value: Int) {
        b[at] = ((value shr 8) and 0xFF).toByte()
        b[at + 1] = (value and 0xFF).toByte()
    }
}
