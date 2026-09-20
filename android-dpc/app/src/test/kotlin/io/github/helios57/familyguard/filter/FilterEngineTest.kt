package io.github.helios57.familyguard.filter

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * The decision, and the two ways it is allowed to be wrong.
 *
 * It may fail open — allow something it could have blocked — as often as a list is incomplete. It
 * may never block the control plane, because the only thing that can switch this filter off is a
 * sync, and a phone that cannot sync is a phone nobody can un-filter without taking it apart.
 */
class FilterEngineTest {

    private val serverUrl = "https://guard.example.com/api"

    @Test
    fun `a name a list blocks is blocked`() {
        val engine = engine("||ads.example.com^")

        val verdict = engine.decideQuery("ads.example.com")

        assertEquals(Verdict.Block("ads.example.com", 3), verdict)
    }

    @Test
    fun `everything under a blocked name is blocked too`() {
        val engine = engine("||doubleclick.net^")

        assertTrue(engine.decideQuery("stats.g.doubleclick.net") is Verdict.Block)
    }

    /**
     * The override that makes this app recoverable. A list is downloaded, so its contents are not
     * something this code gets to promise — and one line naming the control plane would otherwise
     * end every sync, including the one that turns the filter off.
     */
    @Test
    fun `the control plane is never blocked, even by a rule that names it`() {
        val engine = engine("||guard.example.com^")

        assertEquals(Verdict.Allow(AllowReason.NEVER_BLOCKED), engine.decideQuery("guard.example.com"))
    }

    /**
     * And not by a deeper rule either. Protecting the host by adding an allow rule to the index
     * would NOT survive this case: a deeper block beats a shallower allow by design.
     */
    @Test
    fun `a deeper rule cannot reach past the override`() {
        val engine = engine("||sync.guard.example.com^", "@@||guard.example.com^")

        assertEquals(
            Verdict.Allow(AllowReason.NEVER_BLOCKED),
            engine.decideQuery("sync.guard.example.com"),
        )
    }

    /** The protection is a suffix on a label boundary, which is the difference between it and a hole. */
    @Test
    fun `a name that merely ends with the protected host is not protected`() {
        val engine = engine("||evilguard.example.com^")

        assertTrue(engine.decideQuery("evilguard.example.com") is Verdict.Block)
    }

    @Test
    fun `the platform's own lifelines are never blocked`() {
        val engine = engine("||connectivitycheck.gstatic.com^", "||mtalk.google.com^")

        assertEquals(Verdict.Allow(AllowReason.NEVER_BLOCKED), engine.decideQuery("connectivitycheck.gstatic.com"))
        assertEquals(Verdict.Allow(AllowReason.NEVER_BLOCKED), engine.decideQuery("mtalk.google.com"))
    }

    // ---- failing open -----------------------------------------------------------------------

    @Test
    fun `a filter that is off blocks nothing`() {
        val engine = engine("||ads.example.com^", enabled = false)

        assertEquals(Verdict.Allow(AllowReason.FILTER_OFF), engine.decideQuery("ads.example.com"))
        assertEquals(false, engine.isEnabled())
    }

    @Test
    fun `an empty index blocks nothing, even switched on`() {
        val engine = FilterEngine(DomainIndex.EMPTY, FilterEngine.neverBlockedFor(serverUrl), true)

        assertEquals(Verdict.Allow(AllowReason.NO_RULE), engine.decideQuery("ads.example.com"))
        assertEquals("on, but with nothing to enforce", false, engine.isEnabled())
    }

    @Test
    fun `a name that is not a domain is allowed rather than guessed at`() {
        val engine = engine("||ads.example.com^")

        assertEquals(Verdict.Allow(AllowReason.NO_RULE), engine.decideQuery(""))
        assertEquals(Verdict.Allow(AllowReason.NO_RULE), engine.decideQuery("localhost"))
    }

    @Test
    fun `an exception in the list is honoured`() {
        val engine = engine("||example.com^", "@@||cdn.example.com^")

        assertEquals(Verdict.Allow(AllowReason.ALLOWED_BY_RULE), engine.decideQuery("cdn.example.com"))
        assertTrue(engine.decideQuery("ads.example.com") is Verdict.Block)
    }

    // ---- what the parent sees ---------------------------------------------------------------

    @Test
    fun `queries and connections are counted apart`() {
        val engine = engine("||ads.example.com^")

        engine.decideQuery("ads.example.com")
        engine.decideQuery("example.com")
        engine.decideConnection("ads.example.com")
        engine.connectionWithNoName()

        assertEquals(
            FilterCounters(
                queriesSeen = 2,
                queriesBlocked = 1,
                connectionsSeen = 2,
                connectionsBlocked = 1,
                namesHidden = 0,
            ),
            engine.counters(),
        )
    }

    /**
     * ECH does not change the verdict — the public name is the only name there is — but it is the
     * one way this filter goes quietly blind, so it is counted rather than shrugged off.
     */
    @Test
    fun `an encrypted hello is counted without changing the answer`() {
        val engine = engine("||ads.example.com^")

        val verdict = engine.decideConnection("public.example.net", echPresent = true)

        assertEquals(Verdict.Allow(AllowReason.NO_RULE), verdict)
        assertEquals(1, engine.counters().namesHidden)
    }

    @Test
    fun `a new list takes effect without rebuilding the engine`() {
        val engine = engine("||ads.example.com^")
        assertEquals(Verdict.Allow(AllowReason.NO_RULE), engine.decideQuery("tracker.example.org"))

        engine.update(FilterCompiler.compile(sequenceOf("||tracker.example.org^")).first, enabled = true)

        assertTrue(engine.decideQuery("tracker.example.org") is Verdict.Block)
        assertEquals("the old list is gone, not merged", Verdict.Allow(AllowReason.NO_RULE), engine.decideQuery("ads.example.com"))
    }

    // ---- where the protected list comes from ------------------------------------------------

    @Test
    fun `the protected host is derived from the server URL, not configured beside it`() {
        assertEquals("guard.example.com", FilterEngine.neverBlockedFor(serverUrl).first())
    }

    @Test
    fun `a server URL that cannot be parsed still protects the platform's lifelines`() {
        val protectedNames = FilterEngine.neverBlockedFor("not a url at all")

        assertTrue(protectedNames.contains("connectivitycheck.gstatic.com"))
        assertTrue("and nothing invented in place of the host", protectedNames.none { it.contains(' ') })
    }

    private fun engine(vararg lines: String, enabled: Boolean = true): FilterEngine {
        val (index, _) = FilterCompiler.compile(lines.asSequence())
        return FilterEngine(index, FilterEngine.neverBlockedFor(serverUrl), enabled)
    }
}
