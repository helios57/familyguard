package io.github.helios57.familyguard.filter

import java.io.ByteArrayOutputStream
import java.net.InetAddress
import java.net.ServerSocket
import java.net.Socket
import java.util.concurrent.LinkedBlockingQueue
import java.util.concurrent.TimeUnit
import org.junit.After
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * The relay end to end on the JVM: [PacketRouter], [TcpFlow] and the real [NioUpstreamPool] in
 * front of a real loopback server, with a TCP sender written here standing in for the app.
 *
 * [TcpFlowTest] drives the flow machine by hand and calls `upstreamWrote` itself, so it would stay
 * green with that call wired to nothing — which is exactly what shipped until 0.6.44: the method
 * existed, its tests passed, and no production code called it. This test owns no such call. The
 * only way the window it is offered can reopen is the pool reporting real socket writes to the
 * router, and the router passing that on.
 *
 * The stand-in sender is strict in the way that matters: it sends only into the window it was last
 * told about and never probes, so a window that reopens without being announced stalls it. A real
 * Linux sender would eventually probe and recover — after a persist timer that backs off to two
 * minutes, which on a phone reads as "the app hangs". `tools/realtun` is the run with a real stack.
 */
class TcpRelayThroughputTest {

    private val loopback = InetAddress.getByName("127.0.0.1")
    private val server = ServerSocket(0, 50, loopback)
    private val pool = NioUpstreamPool(protectStream = { true }, protectDatagram = { true })
    private val inbox = LinkedBlockingQueue<ByteArray>()

    @After
    fun tearDown() {
        pool.close()
        server.close()
    }

    @Test
    fun `megabytes go both ways on one connection through the real relay and a real socket`() {
        val up = ByteArray(2 shl 20).also { java.util.Random(31).nextBytes(it) }
        val down = ByteArray(2 shl 20).also { java.util.Random(32).nextBytes(it) }

        // The destination: reads everything the app sends, and sends its own two megabytes at the
        // same time, on two threads so neither direction waits on the other.
        val serverReceived = ByteArrayOutputStream()
        val peer = Thread {
            server.accept().use { socket: Socket ->
                val writer = Thread {
                    socket.getOutputStream().write(down)
                    socket.getOutputStream().flush()
                    socket.shutdownOutput()
                }.apply { start() }
                val input = socket.getInputStream()
                val buffer = ByteArray(16 * 1024)
                while (true) {
                    val n = input.read(buffer)
                    if (n < 0) break
                    synchronized(serverReceived) { serverReceived.write(buffer, 0, n) }
                }
                writer.join(30_000)
            }
        }.apply {
            isDaemon = true
            start()
        }

        val router = PacketRouter(
            engine = FilterEngine(FilterCompiler.compile(emptySequence()).first, emptyList(), true),
            dnsTunnel = DnsTunnel(
                FilterEngine(FilterCompiler.compile(emptySequence()).first, emptyList(), true),
                object : DnsRelay {
                    override fun relay(query: ByteArray, onAnswer: (ByteArray) -> Unit) = Unit
                },
            ) {},
            opener = pool,
            toClient = { inbox.add(it) },
            mode = { RouteMode.FULL },
        )
        val app = App(router, server.localPort)
        app.run(up)

        peer.join(10_000)
        assertTrue("the app never saw the destination's FIN", app.finReceived)
        assertArrayEquals(
            "the destination must receive every byte the app sent, in order",
            up,
            synchronized(serverReceived) { serverReceived.toByteArray() },
        )
        assertArrayEquals("the app must receive every byte the destination sent, in order", down, app.received.toByteArray())
    }

    /** A minimal TCP sender and receiver, the app's half of the connection. */
    private inner class App(private val router: PacketRouter, private val serverPort: Int) {
        private val address = byteArrayOf(10, 0, 0, 2)
        private val destination = loopback.address

        val received = ByteArrayOutputStream()
        var finReceived = false
            private set

        private var serverIsn = 0L
        private var sendNext = 0L // offsets into our own stream, past the SYN
        private var sendUnacked = 0L
        private var window = 0
        private var needAck = false

        fun run(body: ByteArray) {
            send(TcpHeader.FLAG_SYN, sequence = ISN, acknowledgement = 0)
            val synAck = parse(inbox.poll(5, TimeUnit.SECONDS) ?: throw AssertionError("no SYN-ACK"))
            check(synAck.second.isSyn && synAck.second.isAck) { "the first answer was not a SYN-ACK" }
            serverIsn = synAck.second.sequenceNumber
            window = synAck.second.windowSize
            send(TcpHeader.FLAG_ACK, sequence = ISN + 1, acknowledgement = serverIsn + 1)

            var finSent = false
            var lastProgress = System.nanoTime()
            while (!(finReceived && finSent && sendUnacked == sendNext)) {
                val before = progress()
                // Send what the window allows.
                while (sendNext < body.size) {
                    val room = window - (sendNext - sendUnacked)
                    if (room <= 0) break
                    val n = minOf(TcpFlow.DEFAULT_MSS.toLong(), room, body.size - sendNext).toInt()
                    send(
                        TcpHeader.FLAG_ACK or TcpHeader.FLAG_PSH,
                        sequence = ISN + 1 + sendNext,
                        acknowledgement = serverIsn + 1 + receivedSequenceSpace(),
                        payload = body.copyOfRange(sendNext.toInt(), sendNext.toInt() + n),
                    )
                    sendNext += n
                    needAck = false
                }
                if (sendNext == body.size.toLong() && !finSent) {
                    send(
                        TcpHeader.FLAG_ACK or TcpHeader.FLAG_FIN,
                        sequence = ISN + 1 + sendNext,
                        acknowledgement = serverIsn + 1 + receivedSequenceSpace(),
                    )
                    sendNext += 1
                    finSent = true
                    needAck = false
                }
                if (needAck) {
                    send(TcpHeader.FLAG_ACK, sequence = ISN + 1 + sendNext, acknowledgement = serverIsn + 1 + receivedSequenceSpace())
                    needAck = false
                }
                val packet = inbox.poll(20, TimeUnit.MILLISECONDS)
                if (packet != null) take(packet)
                while (true) take(inbox.poll() ?: break)

                if (progress() != before) {
                    lastProgress = System.nanoTime()
                } else if (System.nanoTime() - lastProgress > STALL_NANOS) {
                    throw AssertionError(
                        "the connection stalled: sent $sendNext of ${body.size} bytes (acknowledged " +
                            "$sendUnacked), offered window $window; received ${received.size()} bytes, " +
                            "FIN ${if (finReceived) "received" else "not received"}",
                    )
                }
            }
        }

        private fun progress() = Triple(sendUnacked, received.size(), window.toLong() + if (finReceived) 1 else 0)

        private fun receivedSequenceSpace(): Long = received.size().toLong() + if (finReceived) 1 else 0

        private fun take(packet: ByteArray) {
            val (bytes, tcp) = parse(packet)
            if (tcp.isRst) throw AssertionError("the relay reset the connection")
            val acked = (tcp.acknowledgementNumber - (ISN + 1)) and MASK
            if (acked in sendUnacked..sendNext) sendUnacked = acked
            window = tcp.windowSize
            val offset = (tcp.sequenceNumber - (serverIsn + 1)) and MASK
            if (tcp.payloadLength > 0) {
                if (offset == received.size().toLong()) received.write(bytes, tcp.payloadOffset, tcp.payloadLength)
                needAck = true
            }
            if (tcp.isFin && !finReceived && offset + tcp.payloadLength == received.size().toLong()) {
                finReceived = true
                needAck = true
            }
        }

        private fun parse(packet: ByteArray): Pair<ByteArray, TcpHeader> {
            val ip = IpPacket.parse(packet) ?: throw AssertionError("the relay wrote something that is not IP")
            return packet to (TransportHeader.tcp(packet, ip) ?: throw AssertionError("the relay wrote something that is not TCP"))
        }

        private fun send(flags: Int, sequence: Long, acknowledgement: Long, payload: ByteArray = ByteArray(0)) {
            val packet = PacketBuilder.tcpSegment(
                version = 4,
                sourceAddress = address,
                destinationAddress = destination,
                sourcePort = CLIENT_PORT,
                destinationPort = serverPort,
                sequence = sequence and MASK,
                acknowledgement = acknowledgement and MASK,
                flags = flags,
                window = 0xFFFF,
                payload = payload,
            )
            router.handle(packet, packet.size)
        }
    }

    private companion object {
        const val CLIENT_PORT = 40001
        const val ISN = 3_000_000L
        const val MASK = 0xFFFFFFFFL
        const val STALL_NANOS = 5_000_000_000L
    }
}
