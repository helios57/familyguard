package io.github.helios57.familyguard.debug

import java.io.DataInputStream
import java.net.InetAddress
import java.net.ServerSocket
import java.nio.ByteBuffer
import java.nio.ByteOrder
import kotlin.concurrent.thread
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * FR-19.3: the phone finds its own Wireless-debugging adbd on its own loopback, by the adb
 * protocol's answer, without asking the network. Real sockets on this machine's loopback stand in
 * for the listeners a phone has: adbd's TLS port answers a CNXN with STLS, the classic adbd answers
 * with CNXN or AUTH, and anything else answers something else or nothing at all.
 */
class LoopbackAdbProbeTest {

    private val servers = mutableListOf<ServerSocket>()
    private val loopback = InetAddress.getLoopbackAddress()

    @After
    fun close() = servers.forEach { it.close() }

    /** A listener that reads one adb packet header and answers with [reply] — or with nothing. */
    private fun listener(reply: ByteArray?): Int {
        val server = ServerSocket(0, 50, loopback)
        servers += server
        thread(isDaemon = true) {
            while (!server.isClosed) {
                val socket = try {
                    server.accept()
                } catch (e: Exception) {
                    return@thread
                }
                thread(isDaemon = true) {
                    socket.use {
                        runCatching {
                            DataInputStream(it.getInputStream()).readFully(ByteArray(24))
                            if (reply != null) it.getOutputStream().apply { write(reply); flush() } else Thread.sleep(5_000)
                        }
                    }
                }
            }
        }
        return server.localPort
    }

    private fun packet(command: String): ByteArray = ByteBuffer.allocate(24).order(ByteOrder.LITTLE_ENDIAN).apply {
        val cmd = ByteBuffer.wrap(command.toByteArray()).order(ByteOrder.LITTLE_ENDIAN).int
        putInt(cmd); putInt(0x01000000); putInt(0); putInt(0); putInt(0); putInt(cmd.inv())
    }.array()

    private fun probe(ports: List<Int>) = LoopbackAdbProbe(candidates = { ports }, address = loopback)

    @Test
    fun `the port that answers the adb greeting with STLS is adbd's Wireless-debugging port`() {
        val classic = listener(packet("CNXN"))
        val other = listener("HTTP/1.1 400 Bad Request\r\n\r\n".toByteArray())
        val tls = listener(packet("STLS"))
        val silent = listener(null)
        assertEquals(tls, probe(listOf(classic, other, silent, tls)).find())
    }

    @Test
    fun `nothing answering STLS is no port at all`() {
        val classic = listener(packet("AUTH"))
        val other = listener("SSH-2.0-OpenSSH\r\n".toByteArray())
        assertNull(probe(listOf(classic, other)).find())
    }

    @Test
    fun `a listener that never answers costs its read timeout, not the whole search`() {
        val silent = (1..4).map { listener(null) }
        val tls = listener(packet("STLS"))
        val started = System.nanoTime()
        assertEquals(tls, probe(silent + tls).find())
        val millis = (System.nanoTime() - started) / 1_000_000
        assertTrue("four silent listeners held the search for $millis ms", millis < 3_000)
    }

    @Test
    fun `the ephemeral range is read from the kernel's own line`() {
        assertEquals(40000..50999, LoopbackAdbProbe.parseRange("40000\t50999\n"))
        assertEquals(LoopbackAdbProbe.DEFAULT_RANGE, LoopbackAdbProbe.parseRange("garbage"))
    }
}
