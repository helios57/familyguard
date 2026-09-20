package io.github.helios57.familyguard.filter

import java.io.ByteArrayOutputStream

/**
 * The four addresses that identify one TCP connection, from the tunnel's point of view.
 *
 * **Not a data class, and that is the point.** `data class` generates `equals` from each property's
 * own `equals`, and `ByteArray`'s is identity — so two keys built from two copies of the same
 * address would be unequal, every packet of a flow would allocate a new flow, and the machine would
 * never advance past the handshake. It would look like a phone that cannot open a connection.
 */
class FlowKey(
    val version: Int,
    val clientAddress: ByteArray,
    val serverAddress: ByteArray,
    val clientPort: Int,
    val serverPort: Int,
) {
    override fun equals(other: Any?): Boolean =
        other is FlowKey &&
            version == other.version &&
            clientPort == other.clientPort &&
            serverPort == other.serverPort &&
            clientAddress.contentEquals(other.clientAddress) &&
            serverAddress.contentEquals(other.serverAddress)

    override fun hashCode(): Int {
        var result = version
        result = 31 * result + clientPort
        result = 31 * result + serverPort
        result = 31 * result + clientAddress.contentHashCode()
        result = 31 * result + serverAddress.contentHashCode()
        return result
    }

    override fun toString(): String = "${text(clientAddress)}:$clientPort -> ${text(serverAddress)}:$serverPort"

    private fun text(address: ByteArray): String =
        if (address.size == 4) address.joinToString(".") { (it.toInt() and 0xFF).toString() } else "[v6]"
}

/**
 * What the flow machine wants done. It performs no IO itself, so every one of these is a decision
 * the service carries out — which is what makes the whole state machine testable without a socket.
 */
sealed interface FlowEffect {
    /** A finished IP packet to write into the tunnel, towards the app. */
    data class ToClient(val packet: ByteArray) : FlowEffect

    /** Open a `protect()`ed socket to the flow's real destination and start reading from it. */
    data object OpenUpstream : FlowEffect

    /** Bytes for that socket, in order. */
    data class ToUpstream(val bytes: ByteArray) : FlowEffect

    /** The app will send no more. Half-close the upstream socket. */
    data object CloseUpstream : FlowEffect

    /** The flow is over. Drop the socket and forget the key. */
    data object Forget : FlowEffect

    /** What this connection was decided to be, for the counters and the log. */
    data class Decided(val host: String?, val verdict: Verdict) : FlowEffect
}

/**
 * One TCP connection, terminated locally so the name inside it can be read (FR-6.8).
 *
 * A `VpnService` hands the app's packets to this process and gives it no way to forward them: there
 * is no raw socket, so *"inspect and pass on"* is not an option the platform offers. Anything that
 * needs to see inside a TLS handshake therefore has to answer the app's SYN itself, collect the
 * ClientHello, and only then open a real connection to the destination the app asked for. That is
 * what this class is.
 *
 * It buys the one thing DNS filtering cannot: a name for traffic whose name never went through the
 * resolver. An SDK that hardcodes an address, or resolves it over DoH inside its own TLS session,
 * is invisible to every DNS-based filter and completely visible here, because the SNI travels in
 * clear text in the first packet the app sends. That is the difference between blocking the ads in
 * a browser and blocking the ads in a game.
 *
 * ### Why the sequencing is simpler than a real TCP stack, and where it is not
 *
 * The link to the app is a **TUN device on the same machine**, so between the app's kernel and this
 * code there is no loss, no reordering and no duplication — the packet is a buffer handed across a
 * file descriptor. That removes the parts of TCP that exist for a lossy network: no congestion
 * control, no fast retransmit, no out-of-order queue. What it does *not* remove is flow control,
 * because the app can genuinely stop reading, and sequence-number arithmetic, because the app's
 * stack checks every field and silently discards what does not fit its window.
 *
 * Anything that does arrive out of order is answered with a duplicate ACK and dropped. On a
 * loss-free link that costs nothing and cannot happen; if it ever does, the app retransmits and the
 * flow recovers, rather than this code reassembling a case it has no way to test.
 *
 * ### Fail-open, everywhere
 *
 * Every path that cannot reach a verdict opens the connection. A hello that never completes, a
 * protocol this does not recognise, a buffer that fills: all of them splice upstream. The cost of
 * being wrong in that direction is an ad; the cost of being wrong in the other is a child's phone
 * that does not work and a parent who cannot tell why.
 */
class TcpFlow(
    val key: FlowKey,
    private val engine: FilterEngine,
    initialSequence: Long,
    private val maximumSegment: Int = DEFAULT_MSS,
) {

    enum class Phase {
        /** Nothing has arrived yet. */
        NEW,

        /** The SYN-ACK is out; the app's ACK has not arrived. */
        HANDSHAKE,

        /** Connected locally, collecting the bytes the verdict will be taken from. */
        SNIFFING,

        /** Decided to allow. The upstream socket is open, or opening, and bytes flow both ways. */
        OPEN,

        /** Decided to block, or ended. Nothing more is sent. */
        DONE,
    }

    var phase: Phase = Phase.NEW
        private set

    /** The name this flow was decided on, once it has been. Null while undecided or if none. */
    var host: String? = null
        private set

    /** Kept for the life of the flow so a repeated SYN is answered from the same number. */
    private val initialSequence: Long = initialSequence and 0xFFFFFFFFL

    private var sendNext: Long = this.initialSequence
    private var sendUnacked: Long = this.initialSequence
    private var receiveNext: Long = 0
    private var clientWindow: Int = 0
    private var finSent = false
    private var finReceived = false

    /** Everything the app has sent that has not been decided on yet. */
    private val sniffed = ByteArrayOutputStream()

    /** Bytes handed to the service for the upstream socket that it has not reported written. */
    private var upstreamQueued: Int = 0

    /**
     * Our unacknowledged data, kept so a duplicate ACK can be answered.
     *
     * Bounded, and the bound is enforced by not sending past it rather than by discarding: dropping
     * data we have already numbered would desynchronise the connection permanently.
     */
    private val unacked = ByteArrayOutputStream()

    /** What the app may still send us. Shrinks as bytes wait for an upstream socket to take them. */
    private fun advertisedWindow(): Int = (RECEIVE_CAPACITY - upstreamQueued - sniffed.size()).coerceAtLeast(0)

    /** How much more we may put on the wire towards the app before it acknowledges something. */
    private fun sendableNow(): Int =
        (minOf(clientWindow, SEND_CAPACITY) - unacked.size()).coerceAtLeast(0)

    /** True once the service must stop reading from the upstream socket and wait for an ACK. */
    fun upstreamShouldPause(): Boolean = sendableNow() <= 0

    /** The service reports what it actually managed to write, so the window can reopen. */
    fun upstreamWrote(count: Int) {
        upstreamQueued = (upstreamQueued - count).coerceAtLeast(0)
    }

    // ---- from the app --------------------------------------------------------------------------

    fun fromClient(packet: ByteArray, ip: IpHeader, tcp: TcpHeader): List<FlowEffect> {
        val effects = mutableListOf<FlowEffect>()
        if (tcp.isRst) {
            phase = Phase.DONE
            return listOf(FlowEffect.CloseUpstream, FlowEffect.Forget)
        }
        clientWindow = tcp.windowSize

        if (tcp.isSyn && !tcp.isAck) {
            if (phase != Phase.NEW) {
                // A repeated SYN means our SYN-ACK did not arrive or was rejected. Answer it again
                // from the same numbers rather than starting over: a second ISN for one connection
                // is how a flow ends up resetting itself.
                return listOf(FlowEffect.ToClient(synAck()))
            }
            receiveNext = (tcp.sequenceNumber + 1) and 0xFFFFFFFFL
            phase = Phase.HANDSHAKE
            val synAck = synAck()
            // The SYN occupies one sequence number, and BOTH counters move past it. Leaving
            // `sendUnacked` behind would make [acknowledgeOurData] read the app's first ACK as
            // covering one byte of data that does not exist, and every later ACK as covering one
            // byte more than we ever sent — so the outstanding-data buffer would never drain and
            // the send window would close for good, a few kilobytes into the connection.
            sendNext = (sendNext + 1) and 0xFFFFFFFFL
            sendUnacked = sendNext
            return listOf(FlowEffect.ToClient(synAck))
        }

        if (!tcp.isAck) return effects // nothing else is meaningful before the handshake completes

        acknowledgeOurData(tcp.acknowledgementNumber)

        if (phase == Phase.HANDSHAKE) {
            phase = Phase.SNIFFING
            // A port this cannot read a name out of is decided immediately, so a connection to
            // anything that is not web traffic is not held up waiting for bytes that will never
            // say anything.
            if (key.serverPort != PORT_HTTPS && key.serverPort != PORT_HTTP) {
                effects += allow(null)
                if (tcp.payloadLength == 0) return effects
            }
        }

        if (phase == Phase.DONE) return effects

        if (tcp.payloadLength > 0) {
            if (tcp.sequenceNumber != receiveNext) {
                // Already seen, or a gap. Either way: re-state what we are waiting for and drop it.
                return effects + FlowEffect.ToClient(ack())
            }
            receiveNext = (receiveNext + tcp.payloadLength) and 0xFFFFFFFFL
            when (phase) {
                Phase.SNIFFING -> {
                    sniffed.write(packet, tcp.payloadOffset, tcp.payloadLength)
                    effects += FlowEffect.ToClient(ack())
                    effects += decide()
                }
                Phase.OPEN -> {
                    upstreamQueued += tcp.payloadLength
                    effects += FlowEffect.ToUpstream(
                        packet.copyOfRange(tcp.payloadOffset, tcp.payloadOffset + tcp.payloadLength),
                    )
                    effects += FlowEffect.ToClient(ack())
                }
                else -> Unit
            }
        }

        if (tcp.isFin && !finReceived) {
            finReceived = true
            receiveNext = (receiveNext + 1) and 0xFFFFFFFFL
            if (phase == Phase.SNIFFING) {
                // The app finished speaking before a name could be read. Whatever it sent is all
                // there will ever be, so decide on it now rather than holding a half-closed flow.
                effects += decide(final = true)
            }
            effects += FlowEffect.ToClient(ack())
            effects += FlowEffect.CloseUpstream
        }
        return effects
    }

    // ---- from the destination ------------------------------------------------------------------

    /**
     * Bytes the real server sent, on their way to the app.
     *
     * Segmented to [maximumSegment] and clamped to what the app's window allows. The caller must
     * check [upstreamShouldPause] and stop reading rather than calling this with more than fits:
     * data this refuses is data the upstream socket has already consumed, and there is nowhere to
     * put it back.
     */
    fun fromUpstream(bytes: ByteArray, offset: Int = 0, length: Int = bytes.size - offset): List<FlowEffect> {
        if (phase != Phase.OPEN) return emptyList()
        val effects = mutableListOf<FlowEffect>()
        var at = offset
        val end = offset + minOf(length, sendableNow())
        while (at < end) {
            val take = minOf(maximumSegment, end - at)
            effects += FlowEffect.ToClient(
                PacketBuilder.tcpSegment(
                    version = key.version,
                    sourceAddress = key.serverAddress,
                    destinationAddress = key.clientAddress,
                    sourcePort = key.serverPort,
                    destinationPort = key.clientPort,
                    sequence = sendNext,
                    acknowledgement = receiveNext,
                    flags = TcpHeader.FLAG_ACK or TcpHeader.FLAG_PSH,
                    window = advertisedWindow(),
                    payload = bytes,
                    payloadOffset = at,
                    payloadLength = take,
                ),
            )
            unacked.write(bytes, at, take)
            sendNext = (sendNext + take) and 0xFFFFFFFFL
            at += take
        }
        return effects
    }

    /** The destination closed cleanly. Pass that on as a FIN rather than a reset. */
    fun upstreamClosed(): List<FlowEffect> {
        if (phase != Phase.OPEN || finSent) return emptyList()
        finSent = true
        val fin = segment(TcpHeader.FLAG_ACK or TcpHeader.FLAG_FIN)
        sendNext = (sendNext + 1) and 0xFFFFFFFFL
        return listOf(FlowEffect.ToClient(fin))
    }

    /**
     * The destination could not be reached.
     *
     * A reset, not silence. The app gets `ECONNRESET` — an ordinary failure every HTTP client
     * already handles — instead of a socket that hangs until its own timeout, which is the
     * difference between an error and a spinner.
     */
    fun upstreamFailed(): List<FlowEffect> {
        if (phase == Phase.DONE) return emptyList()
        phase = Phase.DONE
        return listOf(FlowEffect.ToClient(reset()), FlowEffect.Forget)
    }

    // ---- the decision ---------------------------------------------------------------------------

    private fun decide(final: Boolean = false): List<FlowEffect> {
        val buffer = sniffed.toByteArray()
        if (key.serverPort == PORT_HTTPS) {
            return when (val hello = TlsClientHello.parse(buffer)) {
                is ClientHello.Found -> verdictFor(hello.serverName, hello.echPresent)
                ClientHello.Incomplete ->
                    // Still arriving. Only give up once there is more of it than any real hello.
                    if (final || buffer.size >= TlsClientHello.MAX_HANDSHAKE_BYTES) allow(null) else emptyList()
                ClientHello.NoName, ClientHello.NotTls -> {
                    engine.connectionWithNoName()
                    allow(null)
                }
            }
        }
        if (key.serverPort == PORT_HTTP) {
            val name = HttpHost.parse(buffer)
            if (name != null) return verdictFor(name, echPresent = false)
            return if (final || !HttpHost.mightBeHttp(buffer)) {
                engine.connectionWithNoName()
                allow(null)
            } else {
                emptyList()
            }
        }
        return allow(null)
    }

    private fun verdictFor(name: String, echPresent: Boolean): List<FlowEffect> {
        host = name
        return when (val verdict = engine.decideConnection(name, echPresent)) {
            is Verdict.Block -> block(name, verdict)
            is Verdict.Allow -> allow(name, verdict)
        }
    }

    private fun block(name: String, verdict: Verdict): List<FlowEffect> {
        phase = Phase.DONE
        return listOf(
            FlowEffect.Decided(name, verdict),
            FlowEffect.ToClient(reset()),
            FlowEffect.Forget,
        )
    }

    private fun allow(
        name: String?,
        verdict: Verdict = Verdict.Allow(AllowReason.NO_RULE),
    ): List<FlowEffect> {
        if (phase == Phase.OPEN || phase == Phase.DONE) return emptyList()
        phase = Phase.OPEN
        val effects = mutableListOf<FlowEffect>(FlowEffect.Decided(name, verdict), FlowEffect.OpenUpstream)
        // Everything collected while deciding is the start of the app's request. It has been
        // acknowledged already, so it exists nowhere else: losing it here would hang the connection
        // with both sides believing the other owes them a byte.
        if (sniffed.size() > 0) {
            val head = sniffed.toByteArray()
            upstreamQueued += head.size
            effects += FlowEffect.ToUpstream(head)
            sniffed.reset()
        }
        return effects
    }

    // ---- segments --------------------------------------------------------------------------------

    /**
     * Drop what the app has acknowledged, reopening that much of the send window.
     *
     * Read as an unsigned distance so the comparison still works across the point where the
     * sequence number wraps past 2^32. Anything that is not a distance into data we actually have
     * outstanding — nothing new, or an acknowledgement of bytes never sent — is ignored rather than
     * acted on: this number is chosen by the peer, and letting it move our own counters is how a
     * connection ends up numbering its next segment outside its own window.
     */
    private fun acknowledgeOurData(acknowledgement: Long) {
        val covered = (acknowledgement - sendUnacked) and 0xFFFFFFFFL
        if (covered == 0L || covered > unacked.size().toLong()) return
        val remaining = unacked.toByteArray()
        unacked.reset()
        unacked.write(remaining, covered.toInt(), remaining.size - covered.toInt())
        sendUnacked = acknowledgement and 0xFFFFFFFFL
    }

    private fun synAck(): ByteArray = PacketBuilder.tcpSegment(
        version = key.version,
        sourceAddress = key.serverAddress,
        destinationAddress = key.clientAddress,
        sourcePort = key.serverPort,
        destinationPort = key.clientPort,
        sequence = initialSequence,
        acknowledgement = receiveNext,
        flags = TcpHeader.FLAG_SYN or TcpHeader.FLAG_ACK,
        window = advertisedWindow(),
    )

    private fun ack(): ByteArray = segment(TcpHeader.FLAG_ACK)

    private fun reset(): ByteArray = PacketBuilder.tcpSegment(
        version = key.version,
        sourceAddress = key.serverAddress,
        destinationAddress = key.clientAddress,
        sourcePort = key.serverPort,
        destinationPort = key.clientPort,
        sequence = sendNext,
        acknowledgement = receiveNext,
        flags = TcpHeader.FLAG_RST or TcpHeader.FLAG_ACK,
        window = 0,
    )

    private fun segment(flags: Int): ByteArray = PacketBuilder.tcpSegment(
        version = key.version,
        sourceAddress = key.serverAddress,
        destinationAddress = key.clientAddress,
        sourcePort = key.serverPort,
        destinationPort = key.clientPort,
        sequence = sendNext,
        acknowledgement = receiveNext,
        flags = flags,
        window = advertisedWindow(),
    )

    companion object {
        const val PORT_HTTP = 80
        const val PORT_HTTPS = 443

        /**
         * 1360 bytes of payload inside the 1400-byte tunnel MTU, leaving 40 for the headers.
         *
         * The tunnel MTU is deliberately below the 1500 of a normal link: a packet this writes
         * travels inside whatever the phone's real connection is, and one that needs fragmenting
         * on the way out is one the peer may never see.
         */
        const val DEFAULT_MSS = 1360

        /** How much the app may have in flight towards us before it has to wait. */
        const val RECEIVE_CAPACITY = 64 * 1024

        /** How much we may have in flight towards the app, on top of whatever window it offers. */
        const val SEND_CAPACITY = 64 * 1024
    }
}
