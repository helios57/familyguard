package io.github.helios57.familyguard.filter

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * The decision layer of the ad filter: which names are blocked, and which lines produce no rule.
 *
 * Every shape here was taken from AdGuard's own DNS filter (`filter_1.txt`, 181,125 lines, measured
 * 2026-09-20), in the proportions it actually has: 180,606 plain `||domain^`, 207 `@@` exceptions,
 * 17 `$`-modified, 15 regex, and 272 partial-anchor or substring rules. The exception count is the
 * one that matters — a compiler that strips `||` and `^` without recognising `@@` first converts
 * each of those 207 into a rule blocking the exact host the list author wrote down to keep working.
 */
class DomainFilterTest {

    // ---- the case that decides whether this filter is safe to ship -----------------------------

    @Test
    fun `an exception is not turned into a block`() {
        val (index, _) = FilterCompiler.compile(
            sequenceOf("||example.com^", "@@||keepworking.example.com^")
        )

        assertTrue("the broad rule still blocks", index.isBlocked("ads.example.com"))
        assertFalse(
            "the exception is what the list author wrote it for",
            index.isBlocked("keepworking.example.com"),
        )
    }

    @Test
    fun `the deeper rule wins, whichever side it is on`() {
        val (index, _) = FilterCompiler.compile(
            sequenceOf("||ads.example.com^", "@@||example.com^", "||other.net^", "@@||cdn.other.net^")
        )

        assertTrue("block at 3 labels beats allow at 2", index.isBlocked("ads.example.com"))
        assertFalse("allow at 3 labels beats block at 2", index.isBlocked("cdn.other.net"))
        assertFalse("the allow still covers the rest of example.com", index.isBlocked("www.example.com"))
        assertTrue("the block still covers the rest of other.net", index.isBlocked("www.other.net"))
    }

    @Test
    fun `an allow and a block on the same domain resolve to allow`() {
        val (index, _) = FilterCompiler.compile(sequenceOf("||same.com^", "@@||same.com^"))

        assertFalse("a tie goes to the exception, as AdGuard's own engine does", index.isBlocked("same.com"))
    }

    // ---- suffix semantics -----------------------------------------------------------------------

    @Test
    fun `a rule covers the domain and everything under it`() {
        val (index, _) = FilterCompiler.compile(sequenceOf("||doubleclick.net^"))

        assertTrue(index.isBlocked("doubleclick.net"))
        assertTrue(index.isBlocked("stats.g.doubleclick.net"))
        assertFalse("a different name that merely ends in the same letters", index.isBlocked("notdoubleclick.net"))
        assertFalse(index.isBlocked("doubleclick.net.example.com"))
    }

    @Test
    fun `a name no rule mentions has no match at all`() {
        val (index, _) = FilterCompiler.compile(sequenceOf("||doubleclick.net^"))

        assertNull("no rule matched is a different fact from allowed by a rule", index.match("example.org"))
        assertFalse(index.isBlocked("example.org"))
    }

    // ---- the input shapes, in the proportions the real list has ---------------------------------

    @Test
    fun `the four accepted shapes all compile to the same rule`() {
        val expected = FilterRule("ads.example.com", allow = false)

        for (line in listOf(
            "||ads.example.com^",
            "ads.example.com",
            "0.0.0.0 ads.example.com",
            "127.0.0.1 ads.example.com",
        )) {
            assertEquals(line, ParseResult.Rule(expected), RuleParser.parse(line))
        }
    }

    @Test
    fun `a hosts line pointing somewhere real is not a block`() {
        // A redirection, not a block. Reading it as a block takes a name the author deliberately
        // re-pointed and makes it unreachable instead.
        assertEquals(
            ParseResult.Skipped(SkipReason.PARTIAL),
            RuleParser.parse("192.168.1.10 nas.example.com"),
        )
    }

    @Test
    fun `everything that cannot be represented is refused and counted, never approximated`() {
        val (index, report) = FilterCompiler.compile(
            sequenceOf(
                "! a comment",
                "# another comment",
                "",
                "example.com##.ad-banner",
                "||tracker.com^\$third-party",
                "/^https?:\\/\\/ads\\./",
                "||bc.geocities.",
                "-iklan1.",
                "://*.a-akamaihd.com^",
                "||good.example^",
            )
        )

        assertEquals("only the one usable rule survives", 1, report.rules)
        assertTrue(index.isBlocked("good.example"))
        assertEquals(3, report.skipped[SkipReason.COMMENT])
        assertEquals(1, report.skipped[SkipReason.COSMETIC])
        assertEquals(1, report.skipped[SkipReason.MODIFIER])
        assertEquals(1, report.skipped[SkipReason.REGEX])
        assertEquals("the three partial-anchor shapes", 3, report.skipped[SkipReason.PARTIAL])
    }

    @Test
    fun `a single label never becomes a rule`() {
        // `com` as a suffix rule is the whole internet. The residue of a mangled line must not be
        // able to produce one.
        assertEquals(ParseResult.Skipped(SkipReason.NOT_A_DOMAIN), RuleParser.parse("com"))
        assertEquals(ParseResult.Skipped(SkipReason.NOT_A_DOMAIN), RuleParser.parse("||localhost^"))
    }

    @Test
    fun `case and a trailing dot do not create two different rules`() {
        val (index, report) = FilterCompiler.compile(sequenceOf("||Ads.Example.COM^", "ads.example.com"))

        assertEquals("the same domain twice is one rule", 1, report.rules)
        assertTrue(index.isBlocked("ADS.EXAMPLE.COM"))
        assertTrue("a query name off the wire is often fully qualified", index.isBlocked("ads.example.com."))
    }

    @Test
    fun `an empty index blocks nothing and does not throw`() {
        assertFalse(DomainIndex.EMPTY.isBlocked("anything.example.com"))
        assertNull(DomainIndex.EMPTY.match("anything.example.com"))
        assertFalse("not a domain, and not a reason to drop traffic", DomainIndex.EMPTY.isBlocked(""))
    }

    @Test
    fun `a name that is not a domain is never blocked`() {
        val (index, _) = FilterCompiler.compile(sequenceOf("||example.com^"))

        for (junk in listOf("", ".", "..", "example .com", "exa mple.com", "-", "a".repeat(300))) {
            assertFalse("junk in is no decision out: $junk", index.isBlocked(junk))
        }
    }
}
