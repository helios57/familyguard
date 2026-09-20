package io.github.helios57.familyguard.filter

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

/**
 * What the parent is shown about the filter, and — the part that matters — what they are NOT shown.
 *
 * Every assertion here is about a null. A console that prints "0 rules" or "off" for a phone that
 * has never measured either is fabricating a measurement, which the project forbids outright; the
 * only way to keep that honest is to decide it somewhere a fixture can drive, which is this class.
 */
class FilterReportTest {

    private fun list(
        url: String = "https://lists.example.com/filter.txt",
        sha: String = "abc",
        rules: Int = 1234,
        fetchedAt: Long = 1_600_000_000_000,
    ) = FilterListState(url = url, sha256 = sha, bytes = 100, rules = rules, fetchedAt = fetchedAt)

    @Test
    fun `a build with no filter reports nothing at all`() {
        val report = FilterReport.of(available = false, list = list(), running = true)
        assertNull("rules", report.rules)
        assertNull("fetched at", report.fetchedAt)
        assertNull("running", report.running)
    }

    @Test
    fun `a build with no filter does not report the tunnel as off`() {
        // "off" is a claim about a switch a Play build does not have. Only null clears the column.
        assertEquals(FilterReport.NOT_AVAILABLE, FilterReport.of(false, list(), running = false))
    }

    @Test
    fun `a fetched list reports its rule count`() {
        assertEquals(1234, FilterReport.of(true, list(rules = 1234), running = true).rules)
    }

    @Test
    fun `a list that was never fetched reports no rule count`() {
        // An empty sha is the store's own "nothing here". Zero would read as a measured zero.
        val report = FilterReport.of(true, FilterListState(), running = false)
        assertNull("rules", report.rules)
        assertNull("fetched at", report.fetchedAt)
        assertEquals(false, report.running)
    }

    @Test
    fun `the fetch time is reported as RFC3339 in UTC`() {
        val report = FilterReport.of(true, list(fetchedAt = 1_600_000_000_000), running = true)
        assertEquals("2020-09-13T12:26:40Z", report.fetchedAt)
    }

    @Test
    fun `a clock that never recorded anything reports no time`() {
        assertNull(FilterReport.at(0))
        assertNull(FilterReport.at(-1))
    }

    @Test
    fun `the running flag is passed through in both directions`() {
        assertEquals(true, FilterReport.of(true, list(), running = true).running)
        assertEquals(false, FilterReport.of(true, list(), running = false).running)
    }

    @Test
    fun `a running tunnel with no list still reports that it is running`() {
        // The two are measured separately and can genuinely disagree: the tunnel can be up on a
        // cache that was cleared. Folding one into the other would hide that from the parent.
        val report = FilterReport.of(true, FilterListState(), running = true)
        assertNull("rules", report.rules)
        assertEquals(true, report.running)
    }

    @Test
    fun `a tunnel that is not running says why`() {
        val report = FilterReport.of(
            true, list(), running = false,
            reason = "the network offers no resolver to forward queries to",
        )
        assertEquals("the network offers no resolver to forward queries to", report.reason)
    }

    @Test
    fun `a tunnel that is up has nothing to explain`() {
        // The reason and the flag are read one after the other, so the service can record a
        // stand-down and then come up before the heartbeat is assembled. Reported together, that
        // would tell a parent the tunnel is running AND that it never started (FR-6.11).
        val report = FilterReport.of(true, list(), running = true, reason = "a reason from before")
        assertEquals("", report.reason)
    }

    @Test
    fun `a tunnel that is off and has recorded nothing says so, rather than saying nothing`() {
        // This is the defect FR-6.11 was extended for, and it was measured on the family phone.
        //
        // The service initialised its reason to "" — the value that means "there is nothing to
        // explain" — so a tunnel that had never run and a tunnel that was up reported the same
        // empty string. The phone reported `running=false, reason=""` for a day with the filter
        // switched on, 180423 rules compiled and a resolver on the network, and the console had
        // nothing to show but a guess, which was wrong.
        //
        // Both spellings of silence are covered: no record at all, and a record that is blank.
        assertEquals(FilterReport.NOTHING_RECORDED,
            FilterReport.of(true, list(), running = false, reason = null).reason)
        assertEquals(FilterReport.NOTHING_RECORDED,
            FilterReport.of(true, list(), running = false, reason = "").reason)
    }

    @Test
    fun `a reason is never blank while the tunnel is not running`() {
        // The general form of the assertion above, stated as the invariant the console depends on:
        // it draws the phone's own words when there are any and guesses when there are not, so a
        // blank is what sends a parent to fix something that is not broken.
        for (reason in listOf(null, "", "   ")) {
            val report = FilterReport.of(true, list(), running = false, reason = reason)
            assertEquals("a filter that is off must always say why (reason=${reason.orEmpty()})",
                false, report.reason.isNullOrBlank())
        }
    }

    @Test
    fun `a build with no filter reports no reason at all`() {
        assertNull(FilterReport.of(false, list(), running = null, reason = "anything").reason)
    }
}
