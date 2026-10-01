package io.github.helios57.familyguard.debug

import java.net.InetAddress
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * The decisions of the phone's remote-adb relay (FR-19.3), without a phone.
 *
 * Every lookup in [AdbdRoute.locate] costs seconds — a loopback sweep, an mDNS listen, a settings
 * write and the wait for adbd to start — so the order is behaviour, not style: a step that runs when
 * it need not makes the parent wait, and a step skipped is a session that fails on a phone where
 * adb was reachable. Each test records the calls made and asserts the sequence.
 */
class RemoteDebugDecisionTest {

    private val calls = mutableListOf<String>()
    private val own = AdbPortFinder.Found(InetAddress.getByName("192.168.1.20"), 37099)

    /** A phone whose loopback answers the n-th time it is probed (1-based), or never. */
    private fun locate(
        service: AdbPortFinder.Service = AdbPortFinder.Service.CONNECT,
        explicitPort: Int = 0,
        loopbackAnswersOn: Int? = null,
        mdnsAnswersWithTimeout: Long? = null,
        switchOn: String? = null,
    ): AdbdRoute {
        var probes = 0
        return AdbdRoute.locate(
            service = service,
            explicitPort = explicitPort,
            probe = {
                probes++
                calls += "probe"
                if (probes == loopbackAnswersOn) 41234 else null
            },
            announced = { timeout ->
                calls += "mdns($timeout)"
                if (timeout == mdnsAnswersWithTimeout) own else null
            },
            switchOn = {
                calls += "switchOn"
                switchOn
            },
            enableWaitMillis = RemoteDebug.ENABLE_WAIT_MILLIS,
        )
    }

    private val mdns = "mdns(${AdbPortFinder.DEFAULT_TIMEOUT_MILLIS})"
    private val mdnsAfterSwitch = "mdns(${RemoteDebug.ENABLE_WAIT_MILLIS})"

    @Test
    fun `a port the command names is used as given and nothing is looked up`() {
        assertEquals(AdbdRoute.Given(5555), locate(explicitPort = 5555, loopbackAnswersOn = 1))
        assertEquals(emptyList<String>(), calls)
        assertEquals("given", AdbdRoute.Given(5555).foundBy)
    }

    @Test
    fun `the loopback is asked first, and an answer there ends the search`() {
        assertEquals(AdbdRoute.Loopback(41234), locate(loopbackAnswersOn = 1, mdnsAnswersWithTimeout = AdbPortFinder.DEFAULT_TIMEOUT_MILLIS))
        assertEquals(listOf("probe"), calls)
    }

    @Test
    fun `mDNS is second, and Wireless debugging is not touched when it answers`() {
        assertEquals(AdbdRoute.Announced(own), locate(mdnsAnswersWithTimeout = AdbPortFinder.DEFAULT_TIMEOUT_MILLIS))
        assertEquals(listOf("probe", mdns), calls)
    }

    @Test
    fun `only when both find nothing is Wireless debugging switched on, then mDNS waits for adbd`() {
        assertEquals(AdbdRoute.Announced(own), locate(mdnsAnswersWithTimeout = RemoteDebug.ENABLE_WAIT_MILLIS))
        assertEquals(listOf("probe", mdns, "switchOn", mdnsAfterSwitch), calls)
    }

    @Test
    fun `a phone that never announces is found by a second sweep of the loopback`() {
        assertEquals(AdbdRoute.Loopback(41234), locate(loopbackAnswersOn = 2))
        assertEquals(listOf("probe", mdns, "switchOn", mdnsAfterSwitch, "probe"), calls)
    }

    @Test
    fun `a refusal to switch on ends the search in the platform's own words`() {
        val refused = "the platform kept Wireless debugging off"
        assertEquals(AdbdRoute.Missing(refused), locate(switchOn = refused, loopbackAnswersOn = 2))
        assertEquals(listOf("probe", mdns, "switchOn"), calls)
    }

    @Test
    fun `nothing anywhere is not found, with no note to blame`() {
        assertEquals(AdbdRoute.Missing(null), locate())
        assertEquals(listOf("probe", mdns, "switchOn", mdnsAfterSwitch, "probe"), calls)
    }

    @Test
    fun `pairing asks mDNS only - the probe cannot see it and switching on cannot open its dialog`() {
        assertEquals(
            AdbdRoute.Missing(null),
            locate(service = AdbPortFinder.Service.PAIRING, loopbackAnswersOn = 1),
        )
        assertEquals(listOf(mdns), calls)

        calls.clear()
        assertEquals(
            AdbdRoute.Announced(own),
            locate(service = AdbPortFinder.Service.PAIRING, mdnsAnswersWithTimeout = AdbPortFinder.DEFAULT_TIMEOUT_MILLIS),
        )
        assertEquals("mdns", AdbdRoute.Announced(own).foundBy)
    }

    // --- the request ---

    private fun params(stream: String? = "abc", target: String? = null, port: Int? = null): JsonObject =
        buildJsonObject {
            stream?.let { put("stream", it) }
            target?.let { put("target", it) }
            port?.let { put("port", it) }
        }

    private fun read(params: JsonObject, open: Int = 0) =
        DebugRequest.of(params, openStreams = open, maxStreams = RemoteDebug.MAX_STREAMS)

    @Test
    fun `a request with nothing but a stream id connects, by lookup`() {
        val request = read(params()) as DebugRequest.Open
        assertEquals(DebugRequest.Open(stream = "abc", target = "connect", explicitPort = 0), request)
        assertEquals(AdbPortFinder.Service.CONNECT, request.service)
    }

    @Test
    fun `pair selects the pairing service, and a port is carried through`() {
        val request = read(params(target = "pair", port = 40001)) as DebugRequest.Open
        assertEquals(AdbPortFinder.Service.PAIRING, request.service)
        assertEquals(40001, request.explicitPort)
    }

    @Test
    fun `a command without a stream id, or for an unknown target, is refused`() {
        assertEquals(DebugRequest.Refused("the command carried no stream id"), read(params(stream = null)))
        assertEquals(DebugRequest.Refused("the command carried no stream id"), read(params(stream = "")))
        assertEquals(DebugRequest.Refused("unknown debug target 'shell'"), read(params(target = "shell")))
    }

    @Test
    fun `the cap allows the fourth stream and refuses the fifth`() {
        val max = RemoteDebug.MAX_STREAMS
        assertEquals(4, max)
        assertTrue(read(params(), open = max - 1) is DebugRequest.Open)
        assertEquals(
            DebugRequest.Refused("$max debug streams are already open on this phone"),
            read(params(), open = max),
        )
    }
}
