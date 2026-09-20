package io.github.helios57.familyguard.filter

import java.io.Closeable
import java.net.DatagramPacket
import java.net.DatagramSocket
import java.net.InetAddress
import java.net.SocketTimeoutException

/**
 * Sends the queries the filter allowed to the resolver the phone was already using.
 *
 * **The upstream list is the network's own, never one this app chose.** A filtering resolver would
 * see every name the family looks up, and picking one on their behalf is a decision about their
 * privacy that belongs to them. When the platform offers no resolver, this refuses to start rather
 * than substituting a public one — see [start].
 *
 * ### The socket must be protected, and failing to protect it is fatal
 *
 * An unprotected socket opened while the tunnel is up is routed *into* the tunnel, so the query
 * this class sends arrives back at the tunnel's own reader, is forwarded again, and the phone spins
 * at 100% CPU until the battery is flat. There is no partial version of this failure and no log
 * line that makes it obvious, which is why [start] returns false rather than carrying on.
 */
class DnsForwarder(
    private val upstream: () -> List<InetAddress>,
    /** `VpnService.protect`. Returns false if the socket would be routed into the tunnel. */
    private val protect: (DatagramSocket) -> Boolean,
    private val clock: () -> Long = System::currentTimeMillis,
    private val log: (String) -> Unit = {},
    /** Always 53 in production; a test needs a port it is allowed to bind. */
    private val upstreamPort: Int = DNS_PORT,
) : DnsRelay, Closeable {

    private val pending = PendingQueries(clock)

    @Volatile
    private var socket: DatagramSocket? = null

    @Volatile
    private var running = false

    private var receiver: Thread? = null

    /** Which upstream to try; advanced whenever a sweep finds queries nobody answered. */
    @Volatile
    private var preferred = 0

    /**
     * Open the socket and start listening. False means the caller must not bring the tunnel up.
     *
     * The two refusals are *"the platform named no resolver"* and *"the socket could not be kept
     * out of the tunnel"*. Both are reported rather than worked around: the first would mean
     * choosing a resolver for the family, and the second is a CPU-pegging loop.
     */
    fun start(): Boolean {
        if (upstream().isEmpty()) {
            log("no upstream resolver from the platform — not starting")
            return false
        }
        val opened = try {
            DatagramSocket()
        } catch (e: Exception) {
            log("could not open the upstream socket: ${e.javaClass.simpleName}")
            return false
        }
        if (!protect(opened)) {
            opened.close()
            log("could not protect the upstream socket — refusing to start")
            return false
        }
        opened.soTimeout = SWEEP_MILLIS
        socket = opened
        running = true
        receiver = Thread({ receiveLoop(opened) }, "fg-dns-upstream").apply {
            isDaemon = true
            start()
        }
        return true
    }

    override fun relay(query: ByteArray, onAnswer: (ByteArray) -> Unit) {
        val live = socket ?: return
        if (query.size < 2) return
        val originalId = ((query[0].toInt() and 0xFF) shl 8) or (query[1].toInt() and 0xFF)

        val resolvers = upstream()
        if (resolvers.isEmpty()) return
        val target = resolvers[preferred % resolvers.size]

        // Registered before sending, because the answer can arrive on the receiving thread before
        // send() has even returned on a fast local resolver.
        val ourId = synchronized(pending) { pending.register(originalId, onAnswer) } ?: return

        val out = query.copyOf()
        out[0] = ((ourId shr 8) and 0xFF).toByte()
        out[1] = (ourId and 0xFF).toByte()
        try {
            live.send(DatagramPacket(out, out.size, target, upstreamPort))
        } catch (e: Exception) {
            synchronized(pending) { pending.complete(ourId) }
            log("upstream send failed: ${e.javaClass.simpleName}")
        }
    }

    private fun receiveLoop(live: DatagramSocket) {
        val buffer = ByteArray(MAX_RESPONSE_BYTES)
        while (running) {
            val packet = DatagramPacket(buffer, buffer.size)
            try {
                live.receive(packet)
            } catch (_: SocketTimeoutException) {
                sweep()
                continue
            } catch (e: Exception) {
                if (running) log("upstream receive failed: ${e.javaClass.simpleName}")
                return
            }
            deliver(packet)
        }
    }

    private fun deliver(packet: DatagramPacket) {
        // Only the resolvers the platform named may answer. Without this an off-path sender who
        // guesses the port can answer for any name, which is a worse hole than the one this app
        // closes.
        if (upstream().none { it == packet.address }) return
        if (packet.length < 2) return

        val answer = packet.data.copyOfRange(packet.offset, packet.offset + packet.length)
        val ourId = ((answer[0].toInt() and 0xFF) shl 8) or (answer[1].toInt() and 0xFF)
        val waiting = synchronized(pending) { pending.complete(ourId) } ?: return

        answer[0] = ((waiting.originalId shr 8) and 0xFF).toByte()
        answer[1] = (waiting.originalId and 0xFF).toByte()
        try {
            waiting.onAnswer(answer)
        } catch (e: Exception) {
            // One bad write must not end the only thread that delivers answers.
            log("writing an answer back failed: ${e.javaClass.simpleName}")
        }
    }

    private fun sweep() {
        val dropped = synchronized(pending) { pending.expire() }
        if (dropped > 0) {
            // A resolver that stopped answering is a resolver to stop preferring. Rotating here
            // rather than on a timer means it only happens when something actually went unanswered.
            preferred++
            log("$dropped queries went unanswered; trying the next resolver")
        }
    }

    override fun close() {
        running = false
        socket?.close()
        socket = null
        receiver?.interrupt()
        receiver = null
        synchronized(pending) { pending.clear() }
    }

    companion object {
        const val DNS_PORT = 53
        const val SWEEP_MILLIS = 1_000
        /** EDNS0 advertises up to 4096; anything larger is not something this forwards. */
        const val MAX_RESPONSE_BYTES = 4096
    }
}
