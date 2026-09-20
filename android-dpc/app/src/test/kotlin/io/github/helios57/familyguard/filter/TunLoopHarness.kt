package io.github.helios57.familyguard.filter

import java.io.BufferedInputStream
import java.io.BufferedOutputStream
import java.io.DataInputStream
import java.io.File
import java.net.InetAddress

/**
 * The filter, wired up outside Android, so a real kernel and real clients can judge it.
 *
 * Everything else in this package is driven by bytes this repo wrote. That is enough to show the
 * parsers agree with the spec and nothing at all about whether the packets this code *emits* are
 * ones a network stack will accept — and the way that fails has no symptom: a wrong checksum is
 * dropped silently by the peer, the query goes unanswered, and the app just seems slow.
 *
 * So `tools/realtun/` puts a real TUN device in a network namespace and runs this in between real
 * clients and real servers: `dig` against `dnsmasq` for the DNS half, `curl` against a real TLS
 * server for the SNI half. If a checksum, a length, an address or a sequence number is wrong
 * anywhere, the client fails and the test fails with it.
 *
 * **The only thing stubbed is the transport between the tun device and this process** — a
 * length-prefixed pipe rather than the descriptor itself. The bytes are identical; that framing is
 * the one thing these runs do not exercise that the phone does. In particular [PacketRouter],
 * [TcpFlow] and [NioUpstreamPool] are the same classes the service uses, and `protect()` is the
 * only seam that differs, because a network namespace is the isolation instead of a VPN.
 *
 * Arguments: `<rules-file> <upstream-host> <upstream-port> [route-mode]`. An empty rules file is the
 * calibration arm: the same run must then carry the very name the other arm blocks.
 */
object TunLoopHarness {

    @JvmStatic
    fun main(args: Array<String>) {
        if (args.size < 3) {
            System.err.println("usage: TunLoopHarness <rules-file> <upstream-host> <upstream-port> [FULL|DNS_ONLY]")
            return
        }
        val rules = File(args[0]).readLines().asSequence()
        val upstreamHost = InetAddress.getByName(args[1])
        val upstreamPort = args[2].toInt()
        val mode = if (args.size > 3) RouteMode.valueOf(args[3]) else RouteMode.DNS_ONLY

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
        val pool = NioUpstreamPool(
            protectStream = { true },
            protectDatagram = { true },
            log = { System.err.println("harness: $it") },
        )
        val router = PacketRouter(
            engine = engine,
            dnsTunnel = DnsTunnel(engine, forwarder, writeBack),
            opener = pool,
            toClient = writeBack,
            mode = { mode },
            log = { System.err.println("harness: $it") },
        )

        val input = DataInputStream(BufferedInputStream(System.`in`))
        val buffer = ByteArray(MAX_PACKET_BYTES)
        System.err.println("harness: ready, ${index.ruleCount} rules, mode=$mode")
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
            router.handle(buffer, length)
        }
        System.err.println("harness: counters ${router.counters()}")
        router.closeAll()
        pool.close()
        forwarder.close()
        System.err.println("harness: stopped")
    }

    private const val MAX_PACKET_BYTES = 32767
}
