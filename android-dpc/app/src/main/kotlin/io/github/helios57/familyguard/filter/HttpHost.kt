package io.github.helios57.familyguard.filter

/**
 * The `Host:` header of a plain-HTTP request, for the port-80 half of connection filtering.
 *
 * Worth having even though almost nothing is plain HTTP any more, because *ad* traffic is where the
 * exceptions live: tracking pixels, click trackers and a fair number of older SDKs still fire
 * cleartext requests, and those are exactly the hosts a filter list names. Reading one header is a
 * very small price for covering them.
 *
 * Deliberately not a parser. It finds one header in the first request line block and refuses
 * everything that is not obviously that, because a permissive reading here would hand
 * [FilterEngine] a name that came from somewhere other than the request the app is making.
 */
object HttpHost {

    /** How much of a request we are willing to read before giving up on finding a `Host:`. */
    const val MAX_HEADER_BYTES = 8 * 1024

    /**
     * Whether [data] can still become a request with a `Host:` header.
     *
     * Separate from [parse] returning null because the two mean opposite things to a flow machine:
     * *"not yet"* means keep buffering, and *"never"* means decide without a name. Folding them
     * together is the same defect [ClientHello.Incomplete] exists to avoid — a decision taken on a
     * prefix, with nothing afterwards to show it happened.
     */
    fun mightBeHttp(data: ByteArray, offset: Int = 0, length: Int = data.size - offset): Boolean {
        if (length == 0) return true
        // Every HTTP method starts with an uppercase letter, and a TLS record never does: its first
        // byte is a content type in 20..24.
        val first = data[offset].toInt() and 0xFF
        return first in 0x41..0x5A && length < MAX_HEADER_BYTES
    }

    /**
     * The host [data] is addressed to, lowercased and without a port, or null.
     *
     * Null covers both "this is not HTTP" and "the headers are not all here yet"; ask
     * [mightBeHttp] to tell those apart.
     */
    fun parse(data: ByteArray, offset: Int = 0, length: Int = data.size - offset): String? {
        if (length <= 0 || length > MAX_HEADER_BYTES) return null
        // ISO-8859-1 rather than UTF-8: it maps every byte to exactly one character, so no byte
        // sequence can decode to a replacement character and no index can shift. Header values are
        // ASCII, and anything that is not is refused below on its own merits.
        val text = String(data, offset, length, Charsets.ISO_8859_1)
        val headerEnd = text.indexOf("\r\n\r\n")
        // Without the blank line the headers may simply not have arrived, and a `Host:` seen before
        // it could still be followed by another one. Refusing until the block is complete costs one
        // round trip on a request that has none.
        if (headerEnd < 0) return null

        var at = text.indexOf("\r\n")
        if (at < 0 || at > headerEnd) return null
        while (at in 0 until headerEnd) {
            val lineEnd = text.indexOf("\r\n", at + 2).let { if (it < 0) headerEnd else it }
            val line = text.substring(at + 2, lineEnd)
            val colon = line.indexOf(':')
            if (colon > 0 && line.substring(0, colon).equals("Host", ignoreCase = true)) {
                val value = line.substring(colon + 1).trim()
                // An IPv6 literal is bracketed and a name never is, so the brackets come off
                // before anything reads it — left on, they would reach a rule lookup and a
                // parent-visible log. [RuleParser.normalise] then refuses the result for having no
                // dot. A v4 literal survives that check and is meant to: the lists do carry address
                // rules, and an address matching one should be blocked like any other name.
                val withoutPort = if (value.startsWith("[")) {
                    value.substringBefore(']').removePrefix("[")
                } else {
                    value.substringBefore(':')
                }
                return RuleParser.normalise(withoutPort)
            }
            if (lineEnd >= headerEnd) break
            at = lineEnd
        }
        return null
    }
}
