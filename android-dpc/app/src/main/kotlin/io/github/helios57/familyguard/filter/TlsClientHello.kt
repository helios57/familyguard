package io.github.helios57.familyguard.filter

/**
 * What reading the head of a TCP flow told us about the name it is asking for.
 *
 * Four states, and the one that earns its place is [Incomplete]. A parser that returned null for
 * both *"not enough bytes yet"* and *"this will never be a ClientHello"* forces the flow machine to
 * pick one wrong behaviour for both: wait forever on plain HTTP, or give up on a TLS handshake that
 * was merely split across two segments and decide the flow with no name at all. Splitting the
 * handshake is normal — a ClientHello with a post-quantum key share does not fit in one segment on
 * most links — so the second mistake is the common one, and it fails open silently.
 */
sealed interface ClientHello {

    /**
     * A name was found.
     *
     * [echPresent] means the hello also carried an `encrypted_client_hello` extension, and then
     * [serverName] is the **public** name the client chose to show, not the one it is really asking
     * for. It is reported rather than hidden: a filter that treats a decoy as the truth is not
     * wrong about this connection, it is wrong about every connection behind that provider, and the
     * only way a parent ever finds out is by noticing ads that should have stopped.
     */
    data class Found(val serverName: String, val echPresent: Boolean) : ClientHello

    /** A complete ClientHello that named no host. Decide without a name. */
    data object NoName : ClientHello

    /** More bytes are needed before this can be answered. Keep buffering. */
    data object Incomplete : ClientHello

    /**
     * Not a TLS ClientHello, or malformed past the point of reading.
     *
     * The two are folded on purpose: a caller can only do one thing with either, which is to stop
     * waiting and decide the flow without a name.
     */
    data object NotTls : ClientHello
}

/**
 * Pulling the SNI out of the first thing a client says on a TCP connection.
 *
 * This is the layer that makes in-app ad blocking work at all. A DNS answer is a name; a TCP
 * connection is an address, and by the time the packets flow the name is gone — except here, in
 * clear text, in the very first record, because the server needs it to pick a certificate. SNI is
 * therefore the same discriminator the DNS layer has, on the connections that never asked DNS: a
 * pinned SDK with a hardcoded address, a connection reused from a resolver this filter never saw,
 * a DoH client that resolves its own names.
 *
 * **Nothing here throws**, for the same reason as [DnsCodec]: this runs on the tunnel thread, once
 * per flow, on bytes an app chose.
 */
object TlsClientHello {

    private const val RECORD_HANDSHAKE = 0x16
    private const val HANDSHAKE_CLIENT_HELLO = 0x01
    private const val EXT_SERVER_NAME = 0x0000
    private const val EXT_ECH = 0xFE0D
    private const val NAME_TYPE_HOST = 0x00

    /** A record body is capped at 16 KiB by TLS itself; a hello spanning more than this is junk. */
    private const val MAX_HANDSHAKE_BYTES = 64 * 1024

    fun parse(data: ByteArray, offset: Int = 0, length: Int = data.size - offset): ClientHello {
        if (offset < 0 || length < 0 || offset + length > data.size) return ClientHello.NotTls
        if (length < 1) return ClientHello.Incomplete
        if ((data[offset].toInt() and 0xFF) != RECORD_HANDSHAKE) return ClientHello.NotTls
        if (length < 3) return ClientHello.Incomplete
        // Every TLS version since 1.0 writes major 3 in the record's legacy version field, and
        // TLS 1.3 still does for middlebox compatibility. Major anything else is a different
        // protocol wearing a 0x16 first byte.
        if ((data[offset + 1].toInt() and 0xFF) != 3) return ClientHello.NotTls

        val handshake = collectHandshake(data, offset, length) ?: return ClientHello.Incomplete
        return parseHandshake(handshake)
    }

    /**
     * The handshake bytes, reassembled across however many records carry them.
     *
     * A ClientHello is one handshake message but not necessarily one record: an implementation may
     * fragment it, and a large key share routinely makes it exceed one segment even when it does
     * not. Returns null when the records on hand do not yet cover a whole message.
     */
    private fun collectHandshake(data: ByteArray, offset: Int, length: Int): ByteArray? {
        val end = offset + length
        var at = offset
        var collected = ByteArray(0)
        while (true) {
            if (at + 5 > end) return null
            if ((data[at].toInt() and 0xFF) != RECORD_HANDSHAKE) return null
            val recordLength = ((data[at + 3].toInt() and 0xFF) shl 8) or (data[at + 4].toInt() and 0xFF)
            if (recordLength == 0 || recordLength > 16384) return null
            if (at + 5 + recordLength > end) return null
            if (collected.size + recordLength > MAX_HANDSHAKE_BYTES) return null

            val grown = ByteArray(collected.size + recordLength)
            System.arraycopy(collected, 0, grown, 0, collected.size)
            System.arraycopy(data, at + 5, grown, collected.size, recordLength)
            collected = grown
            at += 5 + recordLength

            if (collected.size >= 4) {
                val declared = 4 + u24(collected, 1)
                if (collected.size >= declared) return collected
            }
        }
    }

    private fun parseHandshake(h: ByteArray): ClientHello {
        if (h.size < 4) return ClientHello.Incomplete
        if ((h[0].toInt() and 0xFF) != HANDSHAKE_CLIENT_HELLO) return ClientHello.NotTls
        val bodyEnd = 4 + u24(h, 1)
        if (bodyEnd > h.size) return ClientHello.Incomplete

        // client_version (2) + random (32)
        var p = 4 + 34
        p = skipVector8(h, p, bodyEnd) ?: return ClientHello.NotTls // legacy_session_id
        p = skipVector16(h, p, bodyEnd) ?: return ClientHello.NotTls // cipher_suites
        p = skipVector8(h, p, bodyEnd) ?: return ClientHello.NotTls // legacy_compression_methods

        // TLS 1.2 and earlier allow a hello that simply stops here. No extensions, so no SNI.
        if (p == bodyEnd) return ClientHello.NoName
        if (p + 2 > bodyEnd) return ClientHello.NotTls
        val extensionsEnd = p + 2 + u16(h, p)
        if (extensionsEnd > bodyEnd) return ClientHello.NotTls
        p += 2

        var name: String? = null
        var ech = false
        while (p + 4 <= extensionsEnd) {
            val type = u16(h, p)
            val extensionLength = u16(h, p + 2)
            p += 4
            if (p + extensionLength > extensionsEnd) return ClientHello.NotTls
            when (type) {
                // Not returned on sight: the ECH extension can follow the SNI one, and a name read
                // before it would be reported as authoritative when it is a decoy.
                EXT_SERVER_NAME -> if (name == null) name = readServerName(h, p, p + extensionLength)
                EXT_ECH -> ech = true
            }
            p += extensionLength
        }
        return name?.let { ClientHello.Found(it, ech) } ?: ClientHello.NoName
    }

    private fun readServerName(h: ByteArray, from: Int, to: Int): String? {
        if (from + 2 > to) return null
        val listEnd = minOf(from + 2 + u16(h, from), to)
        var p = from + 2
        while (p + 3 <= listEnd) {
            val nameType = h[p].toInt() and 0xFF
            val nameLength = u16(h, p + 1)
            p += 3
            if (p + nameLength > listEnd) return null
            if (nameType == NAME_TYPE_HOST) return asciiHost(h, p, nameLength)
            p += nameLength
        }
        return null
    }

    /**
     * The host bytes as a lowercase string, or null if they are not a printable ASCII name.
     *
     * RFC 6066 says the value is ASCII and carries no trailing dot; clients send both anyway. A
     * non-ASCII or control byte is refused outright rather than coerced, because the only places
     * this string goes next are a rule lookup and a parent-visible log.
     */
    private fun asciiHost(h: ByteArray, from: Int, length: Int): String? {
        if (length == 0 || length > 255) return null
        val sb = StringBuilder(length)
        for (i in 0 until length) {
            val b = h[from + i].toInt() and 0xFF
            if (b < 0x21 || b > 0x7E) return null
            sb.append(b.toChar().lowercaseChar())
        }
        return sb.toString()
    }

    private fun skipVector8(h: ByteArray, at: Int, end: Int): Int? {
        if (at + 1 > end) return null
        val next = at + 1 + (h[at].toInt() and 0xFF)
        return if (next > end) null else next
    }

    private fun skipVector16(h: ByteArray, at: Int, end: Int): Int? {
        if (at + 2 > end) return null
        val next = at + 2 + u16(h, at)
        return if (next > end) null else next
    }

    private fun u16(b: ByteArray, at: Int): Int =
        ((b[at].toInt() and 0xFF) shl 8) or (b[at + 1].toInt() and 0xFF)

    private fun u24(b: ByteArray, at: Int): Int =
        ((b[at].toInt() and 0xFF) shl 16) or ((b[at + 1].toInt() and 0xFF) shl 8) or (b[at + 2].toInt() and 0xFF)
}
