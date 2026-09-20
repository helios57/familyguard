package io.github.helios57.familyguard.filter

import java.io.BufferedInputStream
import java.io.BufferedOutputStream
import java.io.DataInputStream
import java.io.File
import java.net.InetAddress

/**
 * The filter, wired up outside Android, so a real kernel and a real `dig` can judge it.
 *
 * Everything else in this package is driven by bytes this repo wrote. That is enough to show the
 * parsers agree with the spec and nothing at all about whether the packets this code *emits* are
 * ones a network stack will accept — and the way that fails has no symptom: a wrong checksum is
 * dropped silently by the peer, the query goes unanswered, and the app just seems slow.
 *
 * So `tools/realtun/realtun_test.py` puts a real TUN device in a network namespace, runs a real
 * `dnsmasq` as the resolver and a real `dig` as the client, and runs this in between. If a checksum
 * or a length or an address is wrong anywhere, `dig` times out and the test fails. Nothing is
 * stubbed except the transport between the tun device and this process, which is a length-prefixed
 * pipe rather than the descriptor itself — the bytes are identical, and that framing is the one
 * thing the harness does not exercise that the phone does.
 *
 * Arguments: `<rules-file> <upstream-host> <upstream-port>`. An empty rules file is the calibration
 * arm: the same run must then resolve the very name the other arm blocks.
 */
object TunLoopHarness {

    @JvmStatic
    fun main(args: Array<String>) {
        if (args.size < 3) {
            System.err.println("usage: TunLoopHarness <rules-file> <upstream-host> <upstream-port>")
            return
        }
        val rules = File(args[0]).readLines().asSequence()
        val upstreamHost = InetAddress.getByName(args[1])
        val upstreamPort = args[2].toInt()

        val (index, report) = FilterCompiler.compile(rules)
        System.err.println("harness: compiled $report")

        val output = BufferedOutputStream(System.out)
        val writeBack: (ByteArray) -> Unit = { packet ->
            synchronized(output) {
                output.write((packet.size shr 8) and 0xFF)
                output.write(packet.size and 0xFF)
                output.write(packet)
                output.flush()
            }
        }

        val forwarder = DnsForwarder(
            upstream = { listOf(upstreamHost) },
            protect = { true }, // there is no tunnel to fall into here; the namespace is the isolation
            log = { System.err.println("harness: $it") },
            upstreamPort = upstreamPort,
        )
        if (!forwarder.start()) {
            System.err.println("harness: forwarder refused to start")
            return
        }

        val engine = FilterEngine(
            initialIndex = index,
            neverBlocked = FilterEngine.neverBlockedFor("https://guard.example.com"),
            initiallyEnabled = true,
        )
        val tunnel = DnsTunnel(engine, forwarder, writeBack)

        val input = DataInputStream(BufferedInputStream(System.`in`))
        val buffer = ByteArray(MAX_PACKET_BYTES)
        System.err.println("harness: ready, ${index.ruleCount} rules")
        while (true) {
            val high = try { input.read() } catch (_: Exception) { -1 }
            val low = if (high >= 0) try { input.read() } catch (_: Exception) { -1 } else -1
            if (high < 0 || low < 0) break
            val length = (high shl 8) or low
            if (length <= 0 || length > buffer.size) break
            try {
                input.readFully(buffer, 0, length)
            } catch (_: Exception) {
                break
            }
            val outcome = tunnel.handle(buffer, length)
            System.err.println("harness: $outcome")
        }
        forwarder.close()
        System.err.println("harness: stopped")
    }

    private const val MAX_PACKET_BYTES = 32767
}
