package io.github.helios57.familyguard.filter

import java.io.Closeable
import java.net.DatagramSocket
import java.net.InetAddress
import java.net.InetSocketAddress
import java.net.Socket
import java.nio.ByteBuffer
import java.nio.channels.DatagramChannel
import java.nio.channels.SelectionKey
import java.nio.channels.Selector
import java.nio.channels.SocketChannel
import java.util.ArrayDeque
import java.util.concurrent.ConcurrentLinkedQueue

/**
 * Every socket the tunnel opens on an app's behalf, on one thread.
 *
 * One selector rather than a thread per connection, because a phone opens hundreds of connections
 * in a minute of ordinary use and a thread each would cost more than the filtering does. The whole
 * pool is therefore single-threaded: every callback into [PacketRouter] arrives on this thread, and
 * the router's own lock is what makes that safe against the tunnel's reader thread.
 *
 * ### `protect()` is the entire reason this class exists separately
 *
 * A socket opened by a process whose traffic is being routed into a `VpnService` goes **into the
 * tunnel** — so the filter's own upstream connection would arrive back at the filter, forever.
 * `protect()` is what marks a socket as exempt, and it has to be called on the socket *before* it
 * connects. Nothing about forgetting it fails loudly: the connection simply never completes, the
 * app waits out its timeout, and the phone reads as having no internet. Keeping every socket in one
 * class means there is one place that can be wrong about it, and [protectStream] /
 * [protectDatagram] are checked rather than assumed — a refusal fails the connection here instead
 * of becoming a hang the parent has to diagnose.
 */
class NioUpstreamPool(
    private val protectStream: (Socket) -> Boolean,
    private val protectDatagram: (DatagramSocket) -> Boolean,
    private val log: (String) -> Unit = {},
) : UpstreamOpener, Closeable {

    private val selector: Selector = Selector.open()

    /**
     * Work that must happen on the selector thread.
     *
     * `register` blocks for as long as the selector is inside `select()`, so a router thread that
     * registered its own channel would stall until something else happened to arrive — a deadlock
     * that only appears under load, which is the worst kind to find later.
     */
    private val pending = ConcurrentLinkedQueue<() -> Unit>()

    @Volatile private var running = true

    private val thread = Thread(::loop, "familyguard-upstream").apply {
        isDaemon = true
        start()
    }

    override fun openTcp(address: ByteArray, port: Int, listener: UpstreamListener): Upstream? {
        val channel = try {
            SocketChannel.open()
        } catch (e: Exception) {
            log("could not open a socket: ${e.message}")
            return null
        }
        return try {
            channel.configureBlocking(false)
            if (!protectStream(channel.socket())) {
                channel.close()
                log("protect() refused a socket; it would have been routed back into the tunnel")
                return null
            }
            val connection = TcpConnection(channel, listener)
            channel.connect(InetSocketAddress(InetAddress.getByAddress(address), port))
            submit { channel.register(selector, SelectionKey.OP_CONNECT, connection) }
            connection
        } catch (e: Exception) {
            try {
                channel.close()
            } catch (ignored: Exception) {
                // Already unusable; the failure being reported is the one that matters.
            }
            log("could not connect: ${e.message}")
            null
        }
    }

    override fun openUdp(address: ByteArray, port: Int, listener: UpstreamListener): Upstream? {
        val channel = try {
            DatagramChannel.open()
        } catch (e: Exception) {
            log("could not open a datagram socket: ${e.message}")
            return null
        }
        return try {
            channel.configureBlocking(false)
            if (!protectDatagram(channel.socket())) {
                channel.close()
                log("protect() refused a datagram socket")
                return null
            }
            channel.connect(InetSocketAddress(InetAddress.getByAddress(address), port))
            val connection = UdpConnection(channel, listener)
            submit { channel.register(selector, SelectionKey.OP_READ, connection) }
            connection
        } catch (e: Exception) {
            try {
                channel.close()
            } catch (ignored: Exception) {
                // See above.
            }
            log("could not open a datagram relay: ${e.message}")
            null
        }
    }

    override fun close() {
        running = false
        selector.wakeup()
        thread.join(2_000)
        for (key in selector.keys()) {
            try {
                key.channel().close()
            } catch (ignored: Exception) {
                // Shutting down; a channel that will not close is not worth a second failure.
            }
        }
        try {
            selector.close()
        } catch (ignored: Exception) {
            // As above.
        }
    }

    private fun submit(work: () -> Unit) {
        pending.add(work)
        selector.wakeup()
    }

    private fun loop() {
        val buffer = ByteBuffer.allocate(READ_BUFFER)
        while (running) {
            try {
                selector.select(SELECT_TIMEOUT_MILLIS)
            } catch (e: Exception) {
                if (running) log("selector failed: ${e.message}")
                return
            }
            while (true) (pending.poll() ?: break).invoke()

            val keys = selector.selectedKeys().iterator()
            while (keys.hasNext()) {
                val key = keys.next()
                keys.remove()
                val connection = key.attachment() as? Connection ?: continue
                try {
                    connection.ready(key, buffer)
                } catch (e: Exception) {
                    // CancelledKeyException lands here too: a key cancelled between select() and
                    // this line is an ordinary end-of-connection, not a special case.
                    connection.fail(e.message ?: e.javaClass.simpleName)
                }
            }
        }
    }

    private interface Connection {
        fun ready(key: SelectionKey, buffer: ByteBuffer)
        fun fail(reason: String)
    }

    private inner class TcpConnection(
        private val channel: SocketChannel,
        private val listener: UpstreamListener,
    ) : Upstream, Connection {

        /** Written from the router's thread, drained on the selector's. */
        private val outgoing = ArrayDeque<ByteBuffer>()
        private var connected = false
        private var sendClosed = false
        private var closed = false

        /** Set from the router's thread, read on the selector's. See [Upstream.pauseReading]. */
        @Volatile private var readPaused = false

        override fun pauseReading() {
            if (readPaused) return
            readPaused = true
            submit { interest() }
        }

        override fun resumeReading() {
            if (!readPaused) return
            readPaused = false
            submit { interest() }
        }

        override fun send(bytes: ByteArray) {
            synchronized(outgoing) {
                if (closed) return
                outgoing.add(ByteBuffer.wrap(bytes))
            }
            submit { interest() }
        }

        override fun closeSend() {
            sendClosed = true
            submit { flushThenShutdown() }
        }

        override fun close() {
            if (closed) return
            closed = true
            submit {
                try {
                    channel.close()
                } catch (ignored: Exception) {
                    // Closing is all that was wanted.
                }
            }
        }

        override fun ready(key: SelectionKey, buffer: ByteBuffer) {
            if (key.isConnectable) {
                if (!channel.finishConnect()) return
                connected = true
                listener.onConnected()
                interest()
                return
            }
            if (key.isWritable) flush()
            if (key.isReadable) {
                buffer.clear()
                val read = channel.read(buffer)
                if (read < 0) {
                    // A clean close from the far end. The app is told the same way, as a FIN, so a
                    // finished response reads as finished rather than as a broken connection.
                    key.interestOps(key.interestOps() and SelectionKey.OP_READ.inv())
                    listener.onClosed()
                    return
                }
                if (read > 0) listener.onData(buffer.array(), 0, read)
            }
            interest()
        }

        override fun fail(reason: String) {
            if (closed) return
            closed = true
            try {
                channel.close()
            } catch (ignored: Exception) {
                // The failure being reported is the one that matters.
            }
            listener.onFailed(reason)
        }

        private fun flush() {
            synchronized(outgoing) {
                while (outgoing.isNotEmpty()) {
                    val head = outgoing.peek() ?: return
                    channel.write(head)
                    if (head.hasRemaining()) return
                    outgoing.poll()
                }
            }
            if (sendClosed) shutdownOutput()
        }

        private fun flushThenShutdown() {
            if (!connected) return
            flush()
        }

        private fun shutdownOutput() {
            try {
                channel.shutdownOutput()
            } catch (ignored: Exception) {
                // Half-close is a courtesy; the connection still works without it.
            }
        }

        private fun interest() {
            val key = channel.keyFor(selector) ?: return
            if (!key.isValid) return
            var ops = if (readPaused) 0 else SelectionKey.OP_READ
            if (!connected) {
                ops = SelectionKey.OP_CONNECT
            } else if (synchronized(outgoing) { outgoing.isNotEmpty() }) {
                ops = ops or SelectionKey.OP_WRITE
            }
            key.interestOps(ops)
        }
    }

    private inner class UdpConnection(
        private val channel: DatagramChannel,
        private val listener: UpstreamListener,
    ) : Upstream, Connection {

        private var closed = false

        override fun send(bytes: ByteArray) {
            submit {
                if (closed) return@submit
                try {
                    channel.write(ByteBuffer.wrap(bytes))
                } catch (e: Exception) {
                    fail(e.message ?: e.javaClass.simpleName)
                }
            }
        }

        /** UDP has no half-close, and a relay that stopped on one would drop the reply. */
        override fun closeSend() = Unit

        override fun close() {
            if (closed) return
            closed = true
            submit {
                try {
                    channel.close()
                } catch (ignored: Exception) {
                    // Closing is all that was wanted.
                }
            }
        }

        override fun ready(key: SelectionKey, buffer: ByteBuffer) {
            if (!key.isReadable) return
            buffer.clear()
            val read = channel.read(buffer)
            if (read > 0) listener.onData(buffer.array(), 0, read)
        }

        override fun fail(reason: String) {
            if (closed) return
            closed = true
            try {
                channel.close()
            } catch (ignored: Exception) {
                // As above.
            }
            listener.onFailed(reason)
        }
    }

    companion object {
        /** Bigger than any segment the tunnel will forward, so a read is never split by the buffer. */
        const val READ_BUFFER = 32 * 1024

        /** Short enough that a shutdown is prompt, long enough that idling costs nothing. */
        const val SELECT_TIMEOUT_MILLIS = 500L
    }
}
