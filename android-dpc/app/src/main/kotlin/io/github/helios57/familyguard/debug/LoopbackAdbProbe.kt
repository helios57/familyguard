package io.github.helios57.familyguard.debug

import java.io.File
import java.io.IOException
import java.io.InputStream
import java.net.InetAddress
import java.net.InetSocketAddress
import java.net.Socket
import java.nio.ByteBuffer
import java.nio.ByteOrder
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicInteger

/**
 * Finds this phone's Wireless-debugging adbd on its own loopback (FR-19.3), by the adb protocol's own
 * answer rather than by asking the network.
 *
 * adbd takes a port from the kernel's ephemeral range each time Wireless debugging starts, listens on
 * every address including 127.0.0.1, and answers a client's CNXN with STLS — "switch to TLS" — on
 * that port and on no other: the classic adbd answers CNXN or AUTH, the pairing service expects a TLS
 * hello and answers nothing a CNXN could parse. So the listener in the range that answers STLS is
 * adbd's, and it is this phone's, because loopback never leaves the phone.
 *
 * Why this exists next to [AdbPortFinder]: on the family's Android 13 phone (2026-09-27) mDNS
 * announced the pairing service and never the connection service while the ad filter ran, and the
 * parent had to read the port off the screen. Two emulators (API 33 and 37), with the filter on and
 * with Wireless debugging on for minutes, found it by mDNS every time, so that failure is a property
 * of the real phone or its network and was not reproduced. This probe does not depend on either.
 *
 * Cost: one refused connect per closed port, on loopback, spread over [WORKERS] threads — about a
 * second on the emulator for the 28 232 ports of the default range — and only when a parent asks
 * for a debug stream.
 */
class LoopbackAdbProbe(
    private val candidates: () -> List<Int> = { ephemeralRange().toList() },
    private val address: InetAddress = InetAddress.getLoopbackAddress(),
) {
    /** adbd's Wireless-debugging port, or null when nothing in the range answers STLS in time. */
    fun find(deadlineMillis: Long = DEFAULT_DEADLINE_MILLIS): Int? {
        val ports = candidates()
        if (ports.isEmpty()) return null
        val found = AtomicInteger(0)
        val next = AtomicInteger(0)
        val pool = Executors.newFixedThreadPool(WORKERS.coerceAtMost(ports.size))
        val deadline = System.nanoTime() + TimeUnit.MILLISECONDS.toNanos(deadlineMillis)
        repeat(WORKERS.coerceAtMost(ports.size)) {
            pool.execute {
                while (found.get() == 0 && System.nanoTime() < deadline) {
                    val i = next.getAndIncrement()
                    if (i >= ports.size) return@execute
                    if (answersStls(ports[i])) found.compareAndSet(0, ports[i])
                }
            }
        }
        pool.shutdown()
        pool.awaitTermination(deadlineMillis + READ_TIMEOUT_MILLIS, TimeUnit.MILLISECONDS)
        pool.shutdownNow()
        return found.get().takeIf { it > 0 }
    }

    private fun answersStls(port: Int): Boolean {
        val socket = Socket()
        return try {
            socket.connect(InetSocketAddress(address, port), CONNECT_TIMEOUT_MILLIS)
            socket.soTimeout = READ_TIMEOUT_MILLIS
            socket.getOutputStream().apply { write(CNXN); flush() }
            val head = readHeader(socket.getInputStream()) ?: return false
            ByteBuffer.wrap(head).order(ByteOrder.LITTLE_ENDIAN).int == A_STLS
        } catch (e: IOException) {
            false
        } finally {
            try {
                socket.close()
            } catch (ignored: IOException) {
            }
        }
    }

    private fun readHeader(input: InputStream): ByteArray? {
        val head = ByteArray(HEADER_BYTES)
        var read = 0
        while (read < HEADER_BYTES) {
            val n = input.read(head, read, HEADER_BYTES - read)
            if (n < 0) return null
            read += n
        }
        return head
    }

    companion object {
        /** What Linux uses when /proc says nothing readable: `net.ipv4.ip_local_port_range`'s default. */
        val DEFAULT_RANGE = 32768..60999
        const val DEFAULT_DEADLINE_MILLIS = 4_000L
        private const val WORKERS = 32
        private const val CONNECT_TIMEOUT_MILLIS = 200
        private const val READ_TIMEOUT_MILLIS = 500
        private const val HEADER_BYTES = 24

        private fun command(name: String) = ByteBuffer.wrap(name.toByteArray()).order(ByteOrder.LITTLE_ENDIAN).int
        private val A_CNXN = command("CNXN")
        private val A_STLS = command("STLS")

        /** A CNXN as adb's own client sends it: version, max payload, "host::". */
        private val CNXN: ByteArray = run {
            val payload = "host::\u0000".toByteArray()
            ByteBuffer.allocate(HEADER_BYTES + payload.size).order(ByteOrder.LITTLE_ENDIAN).apply {
                putInt(A_CNXN)
                putInt(0x01000001)
                putInt(256 * 1024)
                putInt(payload.size)
                putInt(payload.sumOf { it.toInt() and 0xff })
                putInt(A_CNXN.inv())
                put(payload)
            }.array()
        }

        fun ephemeralRange(): IntRange = try {
            parseRange(File("/proc/sys/net/ipv4/ip_local_port_range").readText())
        } catch (e: Exception) {
            DEFAULT_RANGE
        }

        fun parseRange(line: String): IntRange {
            val parts = line.trim().split(Regex("\\s+")).mapNotNull { it.toIntOrNull() }
            if (parts.size != 2 || parts[0] !in 1..65535 || parts[1] !in parts[0]..65535) return DEFAULT_RANGE
            return parts[0]..parts[1]
        }
    }
}
