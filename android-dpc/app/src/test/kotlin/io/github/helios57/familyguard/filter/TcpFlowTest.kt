package io.github.helios57.familyguard.filter

import java.io.ByteArrayOutputStream
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * The connection machine, one failure mode at a time.
 *
 * `tools/realtun/realtun_sni_test.py` is the authority on whether this works — it puts a real
 * `curl` and a real TLS server on either side of it and a real Linux stack in between, which is the
 * only thing that can tell a correct flow from one that merely looks correct. What these add is
 * *locality*: the real run says "the connection hung", and a hung connection has a dozen causes.
 * Each case here names one of them, so a red points at a line instead of at a symptom.
 *
 * Neither replaces the other, and the asymmetry is worth stating. Several of these would stay green
 * with the whole machine wired to the wrong tunnel; the real run cannot stay green with any one of
 * them broken, and cannot say which.
 */
class TcpFlowTest {

    private val client = byteArrayOf(10, 0, 0, 2)
    private val server = byteArrayOf(93, 184.toByte(), 216.toByte(), 34)

    /**
     * The port the packet fixtures are addressed to, kept in step with the flow's key by [flow].
     *
     * A field rather than an argument on every builder because the mismatch it prevents is
     * invisible: the machine reads the port off its own key, never off the packet, so a flow on 80
     * driven by fixtures addressed to 443 would pass and mean nothing.
     */
    private var fixturePort = 443

    @Test
    fun `a SYN is answered with a SYN-ACK that acknowledges exactly it`() {
        val flow = flow()

        val effects = flow.accept(syn())

        val reply = packets(effects).single()
        assertTrue("the reply must be a SYN-ACK", reply.isSyn && reply.isAck)
        assertEquals(
            "it must acknowledge the SYN's one sequence number",
            (CLIENT_ISN + 1).toLong(),
            reply.acknowledgementNumber,
        )
        assertEquals("it must come from the server the app dialled", 443, reply.sourcePort)
        assertEquals(CLIENT_PORT, reply.destinationPort)
    }

    /**
     * A SYN arriving twice means our answer did not land. Answering with a *second* initial
     * sequence number is how a connection resets itself: the app's stack sees two different servers.
     */
    @Test
    fun `a repeated SYN is answered from the same sequence number`() {
        val flow = flow()
        val first = packets(flow.accept(syn())).single()

        val second = packets(flow.accept(syn())).single()

        assertEquals(first.sequenceNumber, second.sequenceNumber)
    }

    @Test
    fun `a blocked name resets the connection and the flow is forgotten`() {
        val flow = flow(rules = listOf("||ads.example.com^"))
        handshake(flow)

        val effects = flow.accept(data(clientHello("ads.example.com")))

        val decided = effects.filterIsInstance<FlowEffect.Decided>().single()
        assertEquals("ads.example.com", decided.host)
        assertTrue("the verdict must be a block, not a quiet drop", decided.verdict is Verdict.Block)
        assertTrue("the app must be told, not left waiting", packets(effects).any { it.isRst })
        assertTrue(effects.contains(FlowEffect.Forget))
        assertFalse("nothing may be sent to a blocked destination", effects.any { it is FlowEffect.ToUpstream })
        assertEquals(TcpFlow.Phase.DONE, flow.phase)
    }

    /**
     * The bytes collected while deciding were acknowledged as they arrived, so they exist nowhere
     * else. Dropping them leaves both sides waiting for the other — a hang, not an error.
     */
    @Test
    fun `an allowed name opens upstream and forwards the hello collected while deciding`() {
        val flow = flow(rules = listOf("||ads.example.com^"))
        handshake(flow)
        val hello = clientHello("cdn.example.org")

        val effects = flow.accept(data(hello))

        assertTrue(effects.contains(FlowEffect.OpenUpstream))
        val sent = effects.filterIsInstance<FlowEffect.ToUpstream>().single()
        assertBytes("the whole hello must reach the destination", hello, sent.bytes)
        assertEquals(TcpFlow.Phase.OPEN, flow.phase)
    }

    /**
     * A hello split across segments is the case that leaks ads when it is wrong: decide on the
     * first half and there is no name, so the flow is allowed and nothing records that it was ever
     * a candidate. Splitting is normal — a post-quantum key share does not fit one segment.
     */
    @Test
    fun `a hello split across segments is not decided until all of it has arrived`() {
        val flow = flow(rules = listOf("||ads.example.com^"))
        handshake(flow)
        val hello = clientHello("ads.example.com")
        val half = hello.size / 2

        val first = flow.accept(data(hello.copyOfRange(0, half)))
        assertTrue(
            "half a hello must decide nothing",
            first.none { it is FlowEffect.Decided || it == FlowEffect.OpenUpstream },
        )
        assertEquals(TcpFlow.Phase.SNIFFING, flow.phase)

        val rest = flow.accept(data(hello.copyOfRange(half, hello.size), offset = half))

        assertEquals("ads.example.com", rest.filterIsInstance<FlowEffect.Decided>().single().host)
        assertTrue(packets(rest).any { it.isRst })
    }

    /**
     * Fail-open, on the path that fails open by *waiting*. A stream that keeps looking like a hello
     * still arriving has to be carried eventually, or the connection is held until the app gives
     * up — which is a hang with no error and no log line.
     */
    @Test
    fun `a stream that never completes its hello is carried once it is bigger than any hello`() {
        val flow = flow(rules = listOf("||ads.example.com^"))
        handshake(flow)
        // A record header promising 16383 bytes that never arrive: TlsClientHello keeps answering
        // Incomplete for as long as this is fed, which is exactly the shape that hangs.
        val stalled = byteArrayOf(0x16, 0x03, 0x01, 0x3F, 0xFF.toByte()) + ByteArray(2043) { 0xFF.toByte() }
        val filler = ByteArray(2048) { 0xFF.toByte() }

        var sent = 0
        var opened = false
        var chunk = stalled
        while (sent < TlsClientHello.MAX_HANDSHAKE_BYTES + filler.size) {
            val effects = flow.accept(data(chunk, offset = sent))
            sent += chunk.size
            chunk = filler
            if (effects.contains(FlowEffect.OpenUpstream)) {
                opened = true
                break
            }
        }

        assertTrue("a flow that cannot be decided must be carried, not held", opened)
        assertTrue(
            "waiting past the biggest hello there can be is waiting forever",
            sent <= TlsClientHello.MAX_HANDSHAKE_BYTES + filler.size,
        )
    }

    @Test
    fun `a port no name can be read from is carried without waiting for bytes`() {
        val flow = flow(serverPort = 5223)

        val effects = handshake(flow)

        assertTrue(
            "waiting for a name that will never come costs every such connection a round trip",
            effects.contains(FlowEffect.OpenUpstream),
        )
        assertEquals(TcpFlow.Phase.OPEN, flow.phase)
    }

    @Test
    fun `a plain HTTP request is decided on its Host header`() {
        val flow = flow(rules = listOf("||ads.example.com^"), serverPort = 80)
        handshake(flow)
        val request = "GET /pixel.gif HTTP/1.1\r\nHost: ads.example.com\r\nAccept: */*\r\n\r\n"

        val effects = flow.accept(data(request.toByteArray(Charsets.ISO_8859_1)))

        assertEquals("ads.example.com", effects.filterIsInstance<FlowEffect.Decided>().single().host)
        assertTrue(packets(effects).any { it.isRst })
    }

    @Test
    fun `data after the decision goes upstream and is acknowledged`() {
        val flow = open()
        val body = "POST /track".toByteArray()

        val effects = flow.accept(data(body, offset = openedBytes))

        assertBytes("", body, effects.filterIsInstance<FlowEffect.ToUpstream>().single().bytes)
        val ack = packets(effects).single { it.isAck }
        assertEquals(
            "the acknowledgement must cover everything received so far",
            (CLIENT_ISN + 1 + openedBytes + body.size).toLong(),
            ack.acknowledgementNumber,
        )
    }

    @Test
    fun `the destination's answer comes back segmented to the maximum segment size`() {
        val flow = open(maximumSegment = 100)
        val answer = ByteArray(250) { it.toByte() }

        val effects = flow.fromUpstream(answer)

        assertEquals(
            "a segment bigger than the tunnel MTU is one the peer may never see",
            listOf(100, 100, 50),
            packets(effects).map { it.payloadLength },
        )
        val reassembled = ByteArrayOutputStream()
        for ((packet, header) in packetsWith(effects)) {
            reassembled.write(packet, header.payloadOffset, header.payloadLength)
        }
        assertBytes("the answer must arrive whole and in order", answer, reassembled.toByteArray())
    }

    /**
     * The bug this test exists for, found by reading rather than by running.
     *
     * The SYN occupies a sequence number. If only one of the two send counters moves past it, every
     * acknowledgement is read as covering one byte more than was ever sent, `acknowledgeOurData`
     * refuses all of them, the outstanding buffer never drains and the send window closes for good
     * — a few kilobytes into the connection, which is after every handshake assertion has passed.
     */
    @Test
    fun `the send window reopens as the app acknowledges, over far more data than it holds`() {
        val flow = open(maximumSegment = 1000)
        val chunk = ByteArray(4000) { 0x41 }
        var delivered = 0
        // Our own sequence space starts one past the SYN; the hello was the app's data, not ours.
        var acknowledged = SERVER_ISN + 1

        repeat(40) {
            val bytes = packets(flow.fromUpstream(chunk)).sumOf { it.payloadLength }
            delivered += bytes
            acknowledged += bytes
            // The app reads it all and acknowledges it, as a real one does.
            flow.accept(ack(acknowledgement = acknowledged))
        }

        assertEquals("every byte offered must have been sent", 40 * chunk.size, delivered)
        assertFalse("the window must not be stuck shut", flow.upstreamShouldPause())
    }

    @Test
    fun `an out-of-order segment is refused with a duplicate acknowledgement, never accepted`() {
        val flow = open()
        val gap = "later".toByteArray()

        val effects = flow.accept(data(gap, offset = openedBytes + 500))

        assertTrue("nothing may be forwarded out of order", effects.none { it is FlowEffect.ToUpstream })
        assertEquals(
            "the acknowledgement must still name the byte actually expected",
            (CLIENT_ISN + 1 + openedBytes).toLong(),
            packets(effects).single().acknowledgementNumber,
        )
    }

    @Test
    fun `a FIN is acknowledged and half-closes the destination`() {
        val flow = open()

        val effects = flow.accept(fin(offset = openedBytes))

        assertTrue(effects.contains(FlowEffect.CloseUpstream))
        assertEquals(
            "the FIN occupies a sequence number and must be acknowledged",
            (CLIENT_ISN + 1 + openedBytes + 1).toLong(),
            packets(effects).single { it.isAck }.acknowledgementNumber,
        )
    }

    @Test
    fun `a reset from the app ends the flow without answering it`() {
        val flow = open()

        val effects = flow.accept(rst(offset = openedBytes))

        assertTrue(effects.contains(FlowEffect.Forget))
        assertTrue("answering a reset with a packet is how reset storms start", packets(effects).isEmpty())
    }

    /**
     * A destination that cannot be reached must produce an error, not a silence. A dropped packet
     * is indistinguishable from a bad network: the app retries with backoff and the child watches a
     * spinner, which is the symptom this whole feature must not cause.
     */
    @Test
    fun `an unreachable destination is reported to the app as a reset`() {
        val flow = open()

        val effects = flow.upstreamFailed()

        assertTrue(packets(effects).single().isRst)
        assertTrue(effects.contains(FlowEffect.Forget))
    }

    @Test
    fun `a destination that closes cleanly is passed on as a FIN, not a reset`() {
        val flow = open()

        val packet = packets(flow.upstreamClosed()).single()

        assertTrue(packet.isFin)
        assertFalse("a finished response must not read as a broken one", packet.isRst)
    }

    /**
     * The one-way door. If a downloaded list ever names the control plane, the phone stops syncing
     * and the only thing that can switch the filter off is a sync.
     */
    @Test
    fun `the control plane is never blocked, whatever the list says`() {
        val flow = flow(
            rules = listOf("||guard.example.com^"),
            neverBlocked = FilterEngine.neverBlockedFor("https://guard.example.com"),
        )
        handshake(flow)

        val effects = flow.accept(data(clientHello("guard.example.com")))

        assertTrue(effects.contains(FlowEffect.OpenUpstream))
        assertTrue(effects.none { it is FlowEffect.Decided && it.verdict is Verdict.Block })
    }

    // ---- fixtures -------------------------------------------------------------------------------

    private fun flow(
        rules: List<String> = emptyList(),
        neverBlocked: List<String> = emptyList(),
        serverPort: Int = 443,
        maximumSegment: Int = TcpFlow.DEFAULT_MSS,
    ): TcpFlow {
        fixturePort = serverPort
        val (index, _) = FilterCompiler.compile(rules.asSequence())
        val engine = FilterEngine(index, neverBlocked, initiallyEnabled = true)
        val key = FlowKey(4, client, server, CLIENT_PORT, serverPort)
        return TcpFlow(key, engine, SERVER_ISN.toLong(), maximumSegment)
    }

    /** Through the handshake, leaving the flow ready to be decided. */
    private fun handshake(flow: TcpFlow): List<FlowEffect> {
        flow.accept(syn())
        return flow.accept(ack(acknowledgement = SERVER_ISN + 1))
    }

    /** How many bytes of hello [open] put on the wire, so the fixtures can carry on from there. */
    private var openedBytes = 0

    /** Handshaken, decided and spliced, with a hello already sent. */
    private fun open(maximumSegment: Int = TcpFlow.DEFAULT_MSS): TcpFlow {
        val flow = flow(maximumSegment = maximumSegment)
        handshake(flow)
        val hello = clientHello("cdn.example.org")
        openedBytes = hello.size
        flow.accept(data(hello))
        check(flow.phase == TcpFlow.Phase.OPEN) { "the fixture did not open the flow" }
        return flow
    }

    private fun TcpFlow.accept(packet: ByteArray): List<FlowEffect> {
        val ip = IpPacket.parse(packet) ?: throw AssertionError("the fixture is not an IP packet")
        val tcp = TransportHeader.tcp(packet, ip) ?: throw AssertionError("the fixture is not TCP")
        return fromClient(packet, ip, tcp)
    }

    private fun syn() = segment(TcpHeader.FLAG_SYN, CLIENT_ISN, 0)

    private fun ack(acknowledgement: Int) =
        segment(TcpHeader.FLAG_ACK, CLIENT_ISN + 1, acknowledgement)

    private fun data(payload: ByteArray, offset: Int = 0) = segment(
        TcpHeader.FLAG_ACK or TcpHeader.FLAG_PSH,
        CLIENT_ISN + 1 + offset,
        SERVER_ISN + 1,
        payload,
    )

    private fun fin(offset: Int) = segment(
        TcpHeader.FLAG_ACK or TcpHeader.FLAG_FIN,
        CLIENT_ISN + 1 + offset,
        SERVER_ISN + 1,
    )

    private fun rst(offset: Int) = segment(
        TcpHeader.FLAG_RST,
        CLIENT_ISN + 1 + offset,
        SERVER_ISN + 1,
    )

    private fun segment(
        flags: Int,
        sequence: Int,
        acknowledgement: Int,
        payload: ByteArray = ByteArray(0),
    ): ByteArray = PacketBuilder.tcpSegment(
        version = 4,
        sourceAddress = client,
        destinationAddress = server,
        sourcePort = CLIENT_PORT,
        destinationPort = fixturePort,
        sequence = sequence.toLong(),
        acknowledgement = acknowledgement.toLong(),
        flags = flags,
        window = CLIENT_WINDOW,
        payload = payload,
    )

    private fun packets(effects: List<FlowEffect>): List<TcpHeader> = packetsWith(effects).map { it.second }

    private fun packetsWith(effects: List<FlowEffect>): List<Pair<ByteArray, TcpHeader>> =
        effects.filterIsInstance<FlowEffect.ToClient>().map { effect ->
            val ip = IpPacket.parse(effect.packet)
                ?: throw AssertionError("the filter wrote something that is not an IP packet")
            val tcp = TransportHeader.tcp(effect.packet, ip)
                ?: throw AssertionError("the filter wrote something that is not TCP")
            effect.packet to tcp
        }

    private fun assertBytes(message: String, expected: ByteArray, actual: ByteArray) =
        org.junit.Assert.assertEquals(message, expected.toList(), actual.toList())

    /** A minimal but real ClientHello — the same shape `TlsClientHelloTest` builds. */
    private fun clientHello(serverName: String): ByteArray {
        val name = ByteArray(serverName.length) { serverName[it].code.toByte() }
        val entry = bytes {
            write(0)
            write(u16(name.size))
            write(name)
        }
        val list = bytes {
            write(u16(entry.size))
            write(entry)
        }
        val extensions = bytes {
            write(u16(0x0000))
            write(u16(list.size))
            write(list)
        }
        val body = bytes {
            write(byteArrayOf(0x03, 0x03))
            write(ByteArray(32) { it.toByte() })
            write(0) // no session id
            write(u16(2))
            write(byteArrayOf(0x13, 0x01))
            write(1)
            write(0)
            write(u16(extensions.size))
            write(extensions)
        }
        val handshake = bytes {
            write(0x01)
            write(byteArrayOf((body.size shr 16).toByte(), (body.size shr 8).toByte(), body.size.toByte()))
            write(body)
        }
        return bytes {
            write(byteArrayOf(0x16, 0x03, 0x01))
            write(u16(handshake.size))
            write(handshake)
        }
    }

    private fun bytes(build: ByteArrayOutputStream.() -> Unit): ByteArray =
        ByteArrayOutputStream().apply(build).toByteArray()

    private fun u16(value: Int) = byteArrayOf((value shr 8).toByte(), value.toByte())

    private companion object {
        const val CLIENT_PORT = 40000
        const val CLIENT_ISN = 1_000_000
        const val SERVER_ISN = 7_000_000
        const val CLIENT_WINDOW = 65535
    }
}
