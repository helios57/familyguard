package io.github.helios57.familyguard.filter

import org.junit.After
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import java.net.DatagramPacket
import java.net.DatagramSocket
import java.net.InetAddress
import java.net.SocketAddress
import java.util.concurrent.ArrayBlockingQueue
import java.util.concurrent.TimeUnit

/**
 * The forwarder against a real socket and a stand-in resolver.
 *
 * These are the only tests in the filter package that do I/O, and they do it on purpose: the id
 * rewriting has to survive the answer arriving on another thread, and the thing it protects against
 * — one app being handed another app's answer — cannot be reproduced without two queries actually
 * in flight at once.
 */
class DnsForwarderTest {

    private val loopback: InetAddress = InetAddress.getByName("127.0.0.2")
    private lateinit var resolver: DatagramSocket
    private val seen = ArrayBlockingQueue<Pair<ByteArray, SocketAddress>>(16)
    private var forwarder: DnsForwarder? = null
    private var resolverThread: Thread? = null

    private fun startResolver(answer: (ByteArray) -> ByteArray?) {
        resolver = DatagramSocket(0, loopback)
        resolverThread = Thread {
            val buffer = ByteArray(1500)
            while (!resolver.isClosed) {
                val packet = DatagramPacket(buffer, buffer.size)
                try {
                    resolver.receive(packet)
                } catch (_: Exception) {
                    return@Thread
                }
                val query = packet.data.copyOfRange(packet.offset, packet.offset + packet.length)
                seen.offer(query to packet.socketAddress)
                val reply = answer(query) ?: continue
                resolver.send(DatagramPacket(reply, reply.size, packet.socketAddress))
            }
        }.apply { isDaemon = true; start() }
    }

    private fun forwarder(
        upstream: List<InetAddress> = listOf(loopback),
        protect: (DatagramSocket) -> Boolean = { true },
    ) = DnsForwarder(
        upstream = { upstream },
        protect = protect,
        upstreamPort = resolver.localPort,
    ).also { forwarder = it }

    @After
    fun tearDown() {
        forwarder?.close()
        if (this::resolver.isInitialized) resolver.close()
        resolverThread?.interrupt()
    }

    @Test
    fun `a query reaches the resolver under an id this app owns, and comes back under the app's own`() {
        startResolver { query -> query.copyOf().also { it[2] = 0x81.toByte() } }
        val subject = forwarder()
        assertTrue(subject.start())

        val answers = ArrayBlockingQueue<ByteArray>(4)
        subject.relay(query(originalId = 0xBEEF)) { answers.offer(it) }

        val (sent, _) = seen.poll(5, TimeUnit.SECONDS)!!
        assertNotEquals("the wire id is not the app's", 0xBEEF, id(sent))

        val answer = answers.poll(5, TimeUnit.SECONDS)!!
        assertEquals("the app gets its own id back", 0xBEEF, id(answer))
        assertEquals("and the rest of the answer is untouched", 0x81, answer[2].toInt() and 0xFF)
    }

    /**
     * Two apps that both picked id 1. Forwarded unchanged over one socket, the first answer to
     * arrive would be handed to whichever was found first — an address for a name it never asked
     * about.
     */
    @Test
    fun `two queries with the same id do not cross`() {
        // Nothing is answered automatically, so the test can answer the SECOND query first: a
        // table that ignored ids would hand that answer to the first caller.
        startResolver { null }
        val subject = forwarder()
        assertTrue(subject.start())

        val first = ArrayBlockingQueue<ByteArray>(2)
        val second = ArrayBlockingQueue<ByteArray>(2)
        subject.relay(query(originalId = 1, name = "aaa.example.com")) { first.offer(it) }
        subject.relay(query(originalId = 1, name = "bbb.example.com")) { second.offer(it) }

        val queries = listOf(seen.poll(5, TimeUnit.SECONDS)!!, seen.poll(5, TimeUnit.SECONDS)!!)

        // The FIRST query is the one answered, and that choice is what makes this test bind.
        // Answering the second instead passes either way: with the ids left unrewritten both go
        // out as 1, and the table's own second slot also happens to be 1 — so the right app is
        // answered by coincidence. Measured: the not-rewritten probe leaves that version green.
        val forA = queries.single { String(it.first, Charsets.ISO_8859_1).contains("aaa") }
        resolver.send(DatagramPacket(forA.first, forA.first.size, forA.second))

        assertTrue("the app that asked for aaa is answered", first.poll(5, TimeUnit.SECONDS) != null)
        assertEquals("and the other is still waiting", null, second.poll(500, TimeUnit.MILLISECONDS))
    }

    /** An off-path sender who guesses the port must not get to answer for any name. */
    @Test
    fun `an answer from somewhere nobody asked is ignored`() {
        startResolver { null }
        val subject = forwarder()
        assertTrue(subject.start())

        val answers = ArrayBlockingQueue<ByteArray>(4)
        subject.relay(query(originalId = 0x1234)) { answers.offer(it) }
        val (sent, ourAddress) = seen.poll(5, TimeUnit.SECONDS)!!

        DatagramSocket(0, InetAddress.getByName("127.0.0.9")).use { rogue ->
            rogue.send(DatagramPacket(sent, sent.size, ourAddress))
        }

        assertEquals("nothing delivered", null, answers.poll(1, TimeUnit.SECONDS))
    }

    @Test
    fun `with no resolver from the platform it refuses to start`() {
        startResolver { null }

        assertFalse(forwarder(upstream = emptyList()).start())
    }

    /**
     * An unprotected socket is routed into the tunnel, so the query arrives back at the tunnel's
     * own reader and is forwarded again. The phone pegs a core until the battery is flat, and
     * nothing in the log says so.
     */
    @Test
    fun `a socket that cannot be kept out of the tunnel is fatal, not a warning`() {
        startResolver { null }

        assertFalse(forwarder(protect = { false }).start())
    }

    @Test
    fun `a relay after close does nothing and does not throw`() {
        startResolver { null }
        val subject = forwarder()
        subject.start()
        subject.close()

        var called = false
        subject.relay(query(originalId = 1)) { called = true }

        assertFalse(called)
    }

    @Test
    fun `the query bytes are forwarded unchanged apart from the id`() {
        startResolver { null }
        val subject = forwarder()
        subject.start()
        val original = query(originalId = 0xABCD)

        subject.relay(original) {}

        val (sent, _) = seen.poll(5, TimeUnit.SECONDS)!!
        assertArrayEquals(
            original.copyOfRange(2, original.size),
            sent.copyOfRange(2, sent.size),
        )
    }

    private fun query(originalId: Int, name: String = "ads.example.com"): ByteArray {
        val header = byteArrayOf(
            ((originalId shr 8) and 0xFF).toByte(), (originalId and 0xFF).toByte(),
            0x01, 0x00, 0, 1, 0, 0, 0, 0, 0, 0,
        )
        var body = ByteArray(0)
        for (label in name.split('.')) {
            body += label.length.toByte()
            body += label.toByteArray(Charsets.US_ASCII)
        }
        return header + body + byteArrayOf(0, 0, 1, 0, 1)
    }

    private fun id(bytes: ByteArray) = ((bytes[0].toInt() and 0xFF) shl 8) or (bytes[1].toInt() and 0xFF)
}
