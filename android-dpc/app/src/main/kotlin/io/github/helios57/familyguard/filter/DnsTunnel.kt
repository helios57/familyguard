package io.github.helios57.familyguard.filter

/** How a query that is allowed through reaches a real resolver, and how its answer comes back. */
interface DnsRelay {

    /**
     * Send [query] upstream and hand the answer to [onAnswer].
     *
     * Called from the tunnel thread and expected to return immediately: a relay that blocked here
     * would stall every other app on the phone behind one slow resolver. [onAnswer] therefore
     * arrives on whatever thread the relay uses, which is why the tunnel's writer has to be safe to
     * call from anywhere.
     *
     * A query that gets no answer is simply never passed on. The app retries, exactly as it would
     * if the packet had been lost — which, from its point of view, it was.
     */
    fun relay(query: ByteArray, onAnswer: (ByteArray) -> Unit)
}

/** What the tunnel did with one packet. */
sealed interface TunnelOutcome {

    /** Blocked: an NXDOMAIN was written straight back to the app. */
    data class Answered(val host: String) : TunnelOutcome

    /** Allowed: handed to the relay, and the answer will come back later. */
    data class Relayed(val host: String) : TunnelOutcome

    /**
     * Nothing was done and the packet was left alone.
     *
     * [reason] exists so a parent asking *"why is this app still showing ads"* gets an answer other
     * than silence. It is counted, never acted on.
     */
    data class Passed(val reason: String) : TunnelOutcome
}

/**
 * The DNS half of the tunnel: one packet in, one decision out.
 *
 * Separated from the `VpnService` that owns the file descriptor so that the part with the rules in
 * it can be driven by fixtures. Everything this class touches is a byte array and two callbacks;
 * there is no socket, no descriptor and no Android type anywhere in it.
 *
 * ### Why an unrecognised packet is passed rather than dropped
 *
 * In the routed-DNS configuration nothing but DNS should reach the tunnel at all, so every
 * [TunnelOutcome.Passed] here is either a malformed packet or a surprise. Both are left alone. The
 * alternative — dropping what this code does not understand — makes every gap in the parsers a
 * silently broken app, and the child has no way to report it beyond *"the internet is weird"*.
 */
class DnsTunnel(
    private val engine: FilterEngine,
    private val relay: DnsRelay,
    /** Writes a finished packet back into the tunnel. Must be safe to call from any thread. */
    private val writeBack: (ByteArray) -> Unit,
) {

    /**
     * Handle one packet read off the tunnel.
     *
     * [buffer] is the tunnel's own reusable array, so nothing here may keep a reference to it past
     * the call. The relayed path copies what it needs, which is the one allocation in the hot path
     * and is unavoidable: the answer arrives after the buffer has been overwritten several times.
     */
    fun handle(buffer: ByteArray, length: Int): TunnelOutcome {
        val ip = IpPacket.parse(buffer, 0, length) ?: return TunnelOutcome.Passed("not an IP packet")
        if (ip.protocol != IpHeader.PROTO_UDP) return TunnelOutcome.Passed("not UDP")

        val udp = TransportHeader.udp(buffer, ip) ?: return TunnelOutcome.Passed("not a UDP datagram")
        if (udp.destinationPort != DNS_PORT) return TunnelOutcome.Passed("not port 53")

        val query = DnsCodec.parseQuery(buffer, udp.payloadOffset, udp.payloadLength)
            ?: return TunnelOutcome.Passed("not a single-question query")

        return when (val verdict = engine.decideQuery(query.name)) {
            is Verdict.Block -> {
                val answer = DnsCodec.nxDomainFor(buffer, udp.payloadOffset, query)
                writeBack(PacketBuilder.udpReply(buffer, ip, udp, answer))
                TunnelOutcome.Answered(verdict.host)
            }

            is Verdict.Allow -> {
                // Both copies are needed and neither is avoidable: the answer comes back long after
                // the tunnel has reused this buffer for other packets, and the reply has to be
                // addressed from the request that is no longer there.
                val request = buffer.copyOf(length)
                val questionBytes = buffer.copyOfRange(udp.payloadOffset, udp.payloadOffset + udp.payloadLength)
                relay.relay(questionBytes) { answer ->
                    writeBack(PacketBuilder.udpReply(request, ip, udp, answer))
                }
                TunnelOutcome.Relayed(query.name)
            }
        }
    }

    private companion object {
        const val DNS_PORT = 53
    }
}
