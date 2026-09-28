package io.github.helios57.familyguard.filter

import java.net.InetAddress
import java.net.ServerSocket
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * The ad filter's upstream pool must not keep a connection that is over.
 *
 * Measured on an idle emulator (2026-09-28): with the filter running and the screen off, the pool's
 * thread used 10 min 48 s of CPU in 12 minutes. A diagnostic build logged the selector's keys while it
 * spun — 734,654 loop iterations in 10 seconds — and the culprits were sockets with no interest left
 * and their sending half shut: the destination had closed, the app's FIN had shut our side, and the
 * flow was not yet forgotten. Android's poll(2) selector reports a hang-up on such a socket at once,
 * every time, so `select()` never slept again. (The desktop JDK's selector leaves a key with no
 * interest out of poll(2) altogether, so the spin itself cannot be reproduced here; the property that
 * prevents it can: a connection closed at both ends holds no socket in the pool.)
 */
class NioUpstreamPoolTest {

    private val loopback = InetAddress.getLoopbackAddress()
    private val server = ServerSocket(0, 50, loopback)
    private val pool = NioUpstreamPool(protectStream = { true }, protectDatagram = { true })

    @After
    fun tearDown() {
        pool.close()
        server.close()
    }

    private fun listener(closed: CountDownLatch? = null) = object : UpstreamListener {
        override fun onConnected() = Unit
        override fun onData(bytes: ByteArray, offset: Int, length: Int) = Unit
        override fun onClosed() { closed?.countDown() }
        override fun onFailed(reason: String) { closed?.countDown() }
    }

    private fun eventually(want: Int): Int {
        var got = -1
        repeat(50) {
            got = pool.openSockets()
            if (got == want) return got
            Thread.sleep(20)
        }
        return got
    }

    @Test
    fun `the far end closing and then our side closing leaves no socket behind`() {
        val closed = CountDownLatch(1)
        val upstream = pool.openTcp(loopback.address, server.localPort, listener(closed))!!
        server.accept().close()
        assertTrue("the far end's close never reached the pool", closed.await(5, TimeUnit.SECONDS))
        upstream.closeSend()
        assertEquals("a connection closed at both ends still holds a socket in the selector", 0, eventually(0))
    }

    @Test
    fun `our side closing first and then the far end leaves no socket behind`() {
        val closed = CountDownLatch(1)
        val upstream = pool.openTcp(loopback.address, server.localPort, listener(closed))!!
        val peer = server.accept()
        upstream.closeSend()
        Thread.sleep(200)
        peer.close()
        assertTrue("the far end's close never reached the pool", closed.await(5, TimeUnit.SECONDS))
        assertEquals("a connection closed at both ends still holds a socket in the selector", 0, eventually(0))
    }

    @Test
    fun `a connection whose far end has closed stays open while the app may still send`() {
        // Control: half-closed is not closed. The app can still be sending its request body.
        val closed = CountDownLatch(1)
        pool.openTcp(loopback.address, server.localPort, listener(closed))!!
        server.accept().close()
        assertTrue(closed.await(5, TimeUnit.SECONDS))
        assertEquals(1, eventually(1))
    }
}
