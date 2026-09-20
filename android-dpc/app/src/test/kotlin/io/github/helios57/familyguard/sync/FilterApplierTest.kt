package io.github.helios57.familyguard.sync

import io.github.helios57.familyguard.enforce.DesiredState
import io.github.helios57.familyguard.filter.CompileReport
import io.github.helios57.familyguard.filter.FilterListState
import io.github.helios57.familyguard.filter.RefreshResult
import io.github.helios57.familyguard.policy.AlwaysOnVpnOutcome
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * What one sync does to the ad filter, and — mostly — in which order.
 *
 * The order is the substance here. Every one of the four steps is ordered against a failure it
 * would otherwise cause, and none of those failures is visible from the values returned: a filter
 * that persists last loses a policy change to a service restart, and a filter that starts before
 * its list is fetched comes up refusing to run. So the fake records a transcript, and the
 * assertions are about the transcript.
 */
class FilterApplierTest {

    private val url = "https://lists.example.com/filter.txt"
    private val day = FilterApplier.REFRESH_INTERVAL_MILLIS

    @Test
    fun `switching the filter on persists, fetches, grants consent, then starts`() {
        val gateway = FakeGateway()

        val outcome = applier(gateway).apply(on())

        assertTrue(outcome.toString(), outcome.ok)
        assertEquals(
            listOf("remember(true, $url)", "state", "refresh", "alwaysOn(true)", "start"),
            gateway.transcript,
        )
    }

    @Test
    fun `what the parent asked for is persisted before anything can fail`() {
        val gateway = object : FakeGateway() {
            override fun refresh(): RefreshResult {
                throw IllegalStateException("the sync thread died here")
            }
        }

        runCatching { applier(gateway).apply(on()) }

        // The service can be restarted by the platform between any two of these steps, and it reads
        // what it should be doing from storage. Persisting last would leave a window where a
        // restart brings the filter up for a child whose parent has just switched it off.
        assertEquals("remember(true, $url)", gateway.transcript.first())
    }

    @Test
    fun `switching the filter off stops first and asks for nothing else`() {
        val gateway = FakeGateway()

        val outcome = applier(gateway).apply(DesiredState())

        assertTrue(outcome.toString(), outcome.ok)
        // No list fetch, no network, no read of anything. This is the path a parent takes when
        // something is wrong, and it is the shortest one in the file.
        assertEquals(
            listOf("remember(false, )", "stop", "alwaysOn(false)"),
            gateway.transcript,
        )
    }

    @Test
    fun `a cached list within the day is not fetched again`() {
        val gateway = FakeGateway(
            state = FilterListState(url = url, sha256 = "a".repeat(64), rules = 181_117, fetchedAt = 1_000),
        )

        val outcome = FilterApplier(gateway, now = { 1_000 + day - 1 }).apply(on())

        assertFalse("the list was re-fetched", gateway.transcript.contains("refresh"))
        assertTrue(outcome.summary.contains("rules=181117"))
    }

    @Test
    fun `a list a day old is fetched again`() {
        val gateway = FakeGateway(
            state = FilterListState(url = url, sha256 = "a".repeat(64), rules = 10, fetchedAt = 1_000),
        )

        FilterApplier(gateway, now = { 1_000 + day }).apply(on())

        assertTrue(gateway.transcript.contains("refresh"))
    }

    @Test
    fun `a url the parent just changed is fetched at once`() {
        val gateway = FakeGateway(
            state = FilterListState(url = "https://old.example.com/list.txt", sha256 = "a".repeat(64), rules = 10, fetchedAt = 1_000),
        )

        FilterApplier(gateway, now = { 1_001 }).apply(on())

        // A parent acting. Making them wait a day to see it take effect would read as the console
        // not working.
        assertTrue(gateway.transcript.contains("refresh"))
    }

    @Test
    fun `a clock that moved backwards does not park the next fetch a day away`() {
        val stamped = FilterListState(url = url, sha256 = "a".repeat(64), rules = 10, fetchedAt = 9_000_000_000_000)

        // A phone minutes after a factory reset, before it has learned the real time.
        assertTrue(FilterApplier.isRefreshDue(stamped, url, now = 1_000))
    }

    @Test
    fun `a refresh that failed is reported and the tunnel still starts`() {
        val gateway = object : FakeGateway(
            state = FilterListState(url = url, sha256 = "a".repeat(64), rules = 181_117, fetchedAt = 0),
        ) {
            override fun refresh(): RefreshResult {
                transcript += "refresh"
                return RefreshResult.Failed("could not fetch the list: connection reset")
            }
        }

        val outcome = FilterApplier(gateway, now = { day * 2 }).apply(on())

        assertFalse(outcome.ok)
        assertEquals("could not fetch the list: connection reset", outcome.problems["list"])
        // An out-of-date list filters nearly as well as a current one. Refusing to run over a failed
        // refresh would turn a hotel wifi into a filter that is off.
        assertTrue(gateway.transcript.contains("start"))
        assertTrue(outcome.summary.contains("rules=181117"))
    }

    @Test
    fun `consent is granted before the tunnel is asked to start`() {
        val gateway = FakeGateway()

        applier(gateway).apply(on())

        // Always-on is what grants the VPN consent this app can never obtain by asking, so a start
        // before it is a start the platform refuses.
        assertTrue(gateway.transcript.indexOf("alwaysOn(true)") < gateway.transcript.indexOf("start"))
    }

    @Test
    fun `a platform that refuses always-on is reported`() {
        val gateway = object : FakeGateway() {
            override fun setAlwaysOn(enabled: Boolean): AlwaysOnVpnOutcome {
                transcript += "alwaysOn($enabled)"
                return AlwaysOnVpnOutcome("always-on", failure = "always-on is not supported")
            }
        }

        val outcome = applier(gateway).apply(on())

        assertFalse(outcome.ok)
        assertEquals("always-on is not supported", outcome.problems["always_on"])
    }

    @Test
    fun `failing to clear always-on when switching off is reported too`() {
        val gateway = object : FakeGateway() {
            override fun setAlwaysOn(enabled: Boolean): AlwaysOnVpnOutcome {
                transcript += "alwaysOn($enabled)"
                return AlwaysOnVpnOutcome("always-on", failure = "the platform refused")
            }
        }

        val outcome = applier(gateway).apply(DesiredState())

        // Off is the recovery path, and a recovery path that fails silently is the worst kind: the
        // console would show the filter off while the phone kept the tunnel.
        assertFalse(outcome.ok)
        assertEquals("the platform refused", outcome.problems["always_on"])
    }

    @Test
    fun `no url means no fetch, whatever the switch says`() {
        // The engine already refuses this combination, so it should not reach a device at all —
        // which is exactly why it is asserted: an applier that trusted the switch alone would go to
        // the network on every sync of a phone whose parent never configured a list.
        assertFalse(FilterApplier.isRefreshDue(FilterListState(), url = "", now = 1_000))
        assertFalse(FilterApplier.isRefreshDue(FilterListState(), url = "   ", now = 1_000))
    }

    @Test
    fun `an empty cache is always due`() {
        assertTrue(FilterApplier.isRefreshDue(FilterListState(), url, now = 0))
    }

    private fun applier(gateway: FilterGateway) = FilterApplier(gateway, now = { 0 })

    private fun on() = DesiredState(adFilter = true, adFilterListUrl = url)

    private open class FakeGateway(
        private val state: FilterListState = FilterListState(),
    ) : FilterGateway {
        val transcript = mutableListOf<String>()

        override fun remember(enabled: Boolean, listUrl: String) {
            transcript += "remember($enabled, $listUrl)"
        }

        override fun listState(): FilterListState {
            transcript += "state"
            return state
        }

        override fun refresh(): RefreshResult {
            transcript += "refresh"
            return RefreshResult.Updated(
                FilterListState(url = "https://lists.example.com/filter.txt", sha256 = "b".repeat(64), rules = 42),
                CompileReport(rules = 42, allowRules = 0, skipped = emptyMap()),
            )
        }

        override fun setAlwaysOn(enabled: Boolean): AlwaysOnVpnOutcome {
            transcript += "alwaysOn($enabled)"
            return AlwaysOnVpnOutcome("always-on")
        }

        override fun setRunning(running: Boolean) {
            transcript += if (running) "start" else "stop"
        }
    }
}
