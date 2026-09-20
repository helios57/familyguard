package io.github.helios57.familyguard.filter

/**
 * How much of the phone's traffic the tunnel carries.
 *
 * The two are not a preference, they are different risk profiles, and the product ships both on
 * purpose. See [PacketRouter] for what each one can and cannot see.
 */
enum class RouteMode {
    /**
     * Only the tunnel's own resolver address is routed. Everything else goes around the tunnel and
     * is untouched, so a defect in this code can cost DNS and nothing else.
     */
    DNS_ONLY,

    /**
     * Everything is routed. Required for reading a name out of a connection that never asked the
     * resolver — which is most of what an ad SDK does.
     */
    FULL,
}

/** What the tunnel has done since it came up, for the console and for the watchdog. */
data class RouterCounters(
    val packets: Long = 0,
    val dropped: Long = 0,
    val flowsOpened: Long = 0,
    val flowsBlocked: Long = 0,
    val quicDropped: Long = 0,
    val udpRelayed: Long = 0,
    val upstreamFailures: Long = 0,
)

/**
 * Every packet the phone's apps send, and what becomes of each one.
 *
 * This is the whole decision surface of the filter in one place, which is deliberate: an ad-blocking
 * tunnel fails by *quietly* not carrying something, and a routing table spread across four classes
 * is one nobody can read as a list of what happens to a packet.
 *
 * ### What each protocol gets
 *
 * | | |
 * |---|---|
 * | UDP :53 | Filtered by name, answered NXDOMAIN or relayed. [DnsTunnel] owns this. |
 * | UDP :443 | **Dropped** when [dropQuic]. See below — this is the one deliberate breakage. |
 * | UDP other | Relayed through a protected socket, unfiltered. Nothing in it carries a name. |
 * | TCP :443 | Terminated locally, decided on the SNI, then spliced or reset. [TcpFlow]. |
 * | TCP :80 | The same, decided on the `Host:` header. |
 * | TCP other | Spliced immediately. Waiting for a name that will not come costs a round trip. |
 * | anything else | Dropped. ICMP through a `VpnService` needs a raw socket, which apps do not get. |
 *
 * ### Why QUIC is dropped rather than carried (FR-6.9)
 *
 * A QUIC handshake carries the same server name a TLS one does, and encrypts it — recoverable only
 * by deriving the Initial keys from the connection ID, which is a real piece of cryptography rather
 * than a parser. Carrying QUIC unfiltered would mean the ad SDKs that use it are simply exempt, and
 * those are most of the ones that matter: Google's libraries reach for QUIC first.
 *
 * Dropping it is what makes the SNI half work at all. Every QUIC client treats an unanswered
 * handshake as "this network does not do QUIC" and falls back to TLS over TCP within a few hundred
 * milliseconds — where the name is in clear text in the first packet. The cost is that fallback
 * delay on the first connection to a host; the alternative is a filter with a hole the size of
 * Google's ad network.
 *
 * ### Fail-open is the rule, not the fallback (FR-6.7)
 *
 * Anything this cannot parse, cannot name, or cannot decide is carried. A filter that drops what it
 * does not understand is one that breaks a child's phone in a way no parent can diagnose, and the
 * owner's constraint on this whole feature was that it must not.
 */
class PacketRouter(
    private val engine: FilterEngine,
    private val dnsTunnel: DnsTunnel,
    private val opener: UpstreamOpener,
    private val toClient: (ByteArray) -> Unit,
    private val mode: () -> RouteMode,
    private val dropQuic: Boolean = true,
    private val log: (String) -> Unit = {},
    private val clock: () -> Long = System::currentTimeMillis,
) {

    private val flows = HashMap<FlowKey, Entry>()
    private val udpFlows = HashMap<FlowKey, UdpEntry>()
    private var sequenceSeed: Long = (clock() * 2654435761L) and 0xFFFFFFFFL

    @Volatile private var packets = 0L
    @Volatile private var dropped = 0L
    @Volatile private var flowsOpened = 0L
    @Volatile private var flowsBlocked = 0L
    @Volatile private var quicDropped = 0L
    @Volatile private var udpRelayed = 0L
    @Volatile private var upstreamFailures = 0L

    fun counters(): RouterCounters = RouterCounters(
        packets = packets,
        dropped = dropped,
        flowsOpened = flowsOpened,
        flowsBlocked = flowsBlocked,
        quicDropped = quicDropped,
        udpRelayed = udpRelayed,
        upstreamFailures = upstreamFailures,
    )

    /** How many connections are being carried right now. The watchdog reads it; so does the console. */
    fun openFlows(): Int = synchronized(flows) { flows.size }

    /**
     * One packet off the tunnel.
     *
     * Called from the reader thread and synchronised against the upstream callbacks, because a
     * connection's bytes arrive on a different thread from the packets that asked for them.
     */
    fun handle(buffer: ByteArray, length: Int) {
        packets++
        val ip = IpPacket.parse(buffer, 0, length)
        if (ip == null) {
            dropped++
            return
        }
        when (ip.protocol) {
            IpHeader.PROTO_UDP -> udp(buffer, length, ip)
            IpHeader.PROTO_TCP -> tcp(buffer, ip)
            else -> dropped++
        }
    }

    private fun udp(buffer: ByteArray, length: Int, ip: IpHeader) {
        val udp = TransportHeader.udp(buffer, ip)
        if (udp == null) {
            dropped++
            return
        }
        if (udp.destinationPort == DnsForwarder.DNS_PORT) {
            dnsTunnel.handle(buffer, length)
            return
        }
        if (mode() == RouteMode.DNS_ONLY) {
            // Nothing but the resolver is routed in this mode, so a packet that is not for it got
            // here by a route this did not ask for. Dropping is the honest answer: there is no
            // socket set up to carry it and pretending otherwise would lose it silently anyway.
            dropped++
            return
        }
        if (dropQuic && udp.destinationPort == QUIC_PORT) {
            quicDropped++
            return
        }
        relayUdp(buffer, ip, udp)
    }

    private fun relayUdp(buffer: ByteArray, ip: IpHeader, udp: UdpHeader) {
        val key = keyFor(buffer, ip, udp.sourcePort, udp.destinationPort)
        val payload = buffer.copyOfRange(udp.payloadOffset, udp.payloadOffset + udp.payloadLength)
        synchronized(flows) {
            val existing = udpFlows[key]
            if (existing != null) {
                existing.lastUsed = clock()
                existing.upstream.send(payload)
                udpRelayed++
                return
            }
            val entry = UdpEntry(key, clock())
            val upstream = opener.openUdp(key.serverAddress, key.serverPort, UdpListener(entry))
            if (upstream == null) {
                upstreamFailures++
                dropped++
                return
            }
            entry.upstream = upstream
            udpFlows[key] = entry
            upstream.send(payload)
            udpRelayed++
        }
    }

    private fun tcp(buffer: ByteArray, ip: IpHeader) {
        val tcp = TransportHeader.tcp(buffer, ip)
        if (tcp == null) {
            dropped++
            return
        }
        if (mode() == RouteMode.DNS_ONLY) {
            dropped++
            return
        }
        val key = keyFor(buffer, ip, tcp.sourcePort, tcp.destinationPort)
        synchronized(flows) {
            var entry = flows[key]
            if (entry == null) {
                if (!tcp.isSyn) {
                    // A segment for a connection this tunnel has no record of. It belongs to a
                    // session that predates the tunnel coming up, and there is no state anywhere
                    // to continue it — so end it rather than letting the app wait out its own
                    // timeout on a connection nothing will ever answer.
                    toClient(PacketBuilder.tcpReset(buffer, ip, tcp))
                    dropped++
                    return
                }
                sequenceSeed = (sequenceSeed * 1103515245L + 12345L) and 0xFFFFFFFFL
                entry = Entry(TcpFlow(key, engine, sequenceSeed))
                flows[key] = entry
                flowsOpened++
            }
            apply(entry, entry.flow.fromClient(buffer, ip, tcp))
        }
    }

    /**
     * Carry out what a flow decided.
     *
     * Always under the same lock as the packet that caused it, so a connection's bytes cannot
     * overtake the SYN-ACK that has to precede them.
     */
    private fun apply(entry: Entry, effects: List<FlowEffect>) {
        for (effect in effects) {
            when (effect) {
                is FlowEffect.ToClient -> toClient(effect.packet)
                is FlowEffect.ToUpstream ->
                    if (entry.upstream == null) entry.queued.add(effect.bytes) else entry.upstream?.send(effect.bytes)
                FlowEffect.OpenUpstream -> open(entry)
                FlowEffect.CloseUpstream -> entry.upstream?.closeSend()
                FlowEffect.Forget -> forget(entry)
                is FlowEffect.Decided -> {
                    if (effect.verdict is Verdict.Block) {
                        flowsBlocked++
                        log("blocked ${effect.host} (${entry.flow.key})")
                    }
                }
            }
        }
    }

    private fun open(entry: Entry) {
        if (entry.upstream != null) return
        val key = entry.flow.key
        val upstream = opener.openTcp(key.serverAddress, key.serverPort, TcpListener(entry))
        if (upstream == null) {
            upstreamFailures++
            apply(entry, entry.flow.upstreamFailed())
            return
        }
        entry.upstream = upstream
        // Anything the app sent while the socket was being opened. It has already been
        // acknowledged, so it exists nowhere else.
        for (bytes in entry.queued) upstream.send(bytes)
        entry.queued.clear()
    }

    private fun forget(entry: Entry) {
        entry.upstream?.close()
        entry.upstream = null
        flows.remove(entry.flow.key)
    }

    /** Drop every connection. Called when the tunnel goes down, so no socket outlives it. */
    fun closeAll() {
        synchronized(flows) {
            for (entry in flows.values) entry.upstream?.close()
            flows.clear()
            for (entry in udpFlows.values) entry.upstream.close()
            udpFlows.clear()
        }
    }

    /**
     * Drop relays nothing has used for a while.
     *
     * UDP has no close, so without this every DNS-over-something and every game packet would leave
     * a socket behind for the life of the tunnel, and the phone would run out of descriptors in a
     * way that looks like the network failing.
     */
    fun expire(idleMillis: Long = UDP_IDLE_MILLIS): Int {
        val cutoff = clock() - idleMillis
        var closed = 0
        synchronized(flows) {
            val stale = udpFlows.values.filter { it.lastUsed < cutoff }
            for (entry in stale) {
                entry.upstream.close()
                udpFlows.remove(entry.key)
                closed++
            }
        }
        return closed
    }

    private fun keyFor(buffer: ByteArray, ip: IpHeader, sourcePort: Int, destinationPort: Int): FlowKey =
        FlowKey(
            version = ip.version,
            clientAddress = buffer.copyOfRange(ip.sourceOffset, ip.sourceOffset + ip.addressBytes),
            serverAddress = buffer.copyOfRange(ip.destinationOffset, ip.destinationOffset + ip.addressBytes),
            clientPort = sourcePort,
            serverPort = destinationPort,
        )

    private class Entry(val flow: TcpFlow) {
        var upstream: Upstream? = null

        /** Bytes decided on before the socket existed. Never dropped; the app was told they landed. */
        val queued = ArrayList<ByteArray>(2)
    }

    private class UdpEntry(val key: FlowKey, var lastUsed: Long) {
        lateinit var upstream: Upstream
    }

    private inner class TcpListener(private val entry: Entry) : UpstreamListener {
        override fun onConnected() = Unit

        override fun onData(bytes: ByteArray, offset: Int, length: Int) {
            synchronized(flows) { apply(entry, entry.flow.fromUpstream(bytes, offset, length)) }
        }

        override fun onClosed() {
            synchronized(flows) { apply(entry, entry.flow.upstreamClosed()) }
        }

        override fun onFailed(reason: String) {
            synchronized(flows) {
                upstreamFailures++
                log("upstream failed for ${entry.flow.key}: $reason")
                apply(entry, entry.flow.upstreamFailed())
            }
        }
    }

    private inner class UdpListener(private val entry: UdpEntry) : UpstreamListener {
        override fun onConnected() = Unit

        override fun onData(bytes: ByteArray, offset: Int, length: Int) {
            val key = entry.key
            entry.lastUsed = clock()
            toClient(
                PacketBuilder.udpDatagram(
                    version = key.version,
                    sourceAddress = key.serverAddress,
                    destinationAddress = key.clientAddress,
                    sourcePort = key.serverPort,
                    destinationPort = key.clientPort,
                    payload = bytes,
                    payloadOffset = offset,
                    payloadLength = length,
                ),
            )
        }

        override fun onClosed() {
            synchronized(flows) { udpFlows.remove(entry.key)?.upstream?.close() }
        }

        override fun onFailed(reason: String) {
            synchronized(flows) {
                upstreamFailures++
                udpFlows.remove(entry.key)?.upstream?.close()
            }
        }
    }

    companion object {
        const val QUIC_PORT = 443

        /** Long enough for a slow game server's reply, short enough that descriptors come back. */
        const val UDP_IDLE_MILLIS = 60_000L
    }
}
