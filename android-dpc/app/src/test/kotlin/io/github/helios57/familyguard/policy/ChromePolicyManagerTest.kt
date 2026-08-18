package io.github.helios57.familyguard.policy

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * A managed-configuration store that behaves like the platform's.
 *
 * @param roundTripLists stores a `List` back as an `Array`, which is what a real `Bundle` does. On
 * by default, because a fake that stores lists as lists would let a manager that cannot compare the
 * two pass here and report every apply on a real phone as not stored.
 * @param drop keys the store accepts and does not keep — the accepted-but-not-stored shape.
 */
private class FakeConfigGateway(
    private val drop: Set<String> = emptySet(),
    private val throws: RuntimeException? = null,
    private val roundTripLists: Boolean = true,
) : ManagedConfigGateway {
    private var stored: Map<String, Any> = emptyMap()
    var writes = 0
        private set

    override fun current(pkg: String): Map<String, Any> = stored

    override fun set(pkg: String, config: Map<String, Any>) {
        writes++
        throws?.let { throw it }
        stored = config
            .filterKeys { it !in drop }
            .mapValues { (_, v) ->
                if (roundTripLists && v is List<*>) v.map { it.toString() }.toTypedArray() else v
            }
    }
}

class ChromePolicyManagerTest {

    private val console = listOf("guard.example.com")

    @Test
    fun `composes every key on every apply`() {
        val config = ChromePolicyManager(FakeConfigGateway())
            .compose(listOf("example.com"), safeSearch = true, youtubeRestricted = true, neverBlocked = console)

        // FR-6.4: an absent key is Chrome's default, not a decision. Removing the last blocked
        // domain has to write an empty list, not stop writing the key — otherwise the previous
        // non-empty list stays in force and the parent's removal does nothing.
        assertEquals(
            setOf(
                ChromePolicyManager.KEY_URL_BLOCKLIST,
                ChromePolicyManager.KEY_URL_ALLOWLIST,
                ChromePolicyManager.KEY_SAFE_SEARCH,
                ChromePolicyManager.KEY_YOUTUBE_RESTRICT,
                ChromePolicyManager.KEY_INCOGNITO,
            ),
            config.keys,
        )
        assertEquals(listOf("example.com"), config[ChromePolicyManager.KEY_URL_BLOCKLIST])
        assertEquals(console, config[ChromePolicyManager.KEY_URL_ALLOWLIST])
        assertEquals(true, config[ChromePolicyManager.KEY_SAFE_SEARCH])
        assertEquals(ChromePolicyManager.YOUTUBE_STRICT, config[ChromePolicyManager.KEY_YOUTUBE_RESTRICT])
        assertEquals(ChromePolicyManager.INCOGNITO_DISABLED, config[ChromePolicyManager.KEY_INCOGNITO])
    }

    @Test
    fun `an empty blocklist is still written`() {
        val config = ChromePolicyManager(FakeConfigGateway())
            .compose(emptyList(), safeSearch = false, youtubeRestricted = false, neverBlocked = emptyList())

        assertEquals(emptyList<String>(), config[ChromePolicyManager.KEY_URL_BLOCKLIST])
        assertEquals(false, config[ChromePolicyManager.KEY_SAFE_SEARCH])
        assertEquals(ChromePolicyManager.YOUTUBE_OFF, config[ChromePolicyManager.KEY_YOUTUBE_RESTRICT])
    }

    @Test
    fun `the blocklist is deduplicated, sorted and free of blanks`() {
        val config = ChromePolicyManager(FakeConfigGateway()).compose(
            listOf("b.example", "a.example", "b.example", "", "  "),
            safeSearch = false,
            youtubeRestricted = false,
            neverBlocked = emptyList(),
        )

        assertEquals(listOf("a.example", "b.example"), config[ChromePolicyManager.KEY_URL_BLOCKLIST])
    }

    @Test
    fun `the console is on the allowlist, which wins over the blocklist in Chrome`() {
        val config = ChromePolicyManager(FakeConfigGateway()).compose(
            blockedDomains = listOf("example.com"),
            safeSearch = false,
            youtubeRestricted = false,
            neverBlocked = console,
        )

        // FR-6.5. A parent who blocks the domain the console lives under would otherwise cut the
        // phone off from the only thing that can undo it.
        assertEquals(listOf("example.com"), config[ChromePolicyManager.KEY_URL_BLOCKLIST])
        assertEquals(console, config[ChromePolicyManager.KEY_URL_ALLOWLIST])
    }

    @Test
    fun `a list stored back as an array still reads as applied`() {
        val gateway = FakeConfigGateway(roundTripLists = true)

        val outcome = ChromePolicyManager(gateway)
            .apply(listOf("example.com"), safeSearch = true, youtubeRestricted = true, neverBlocked = console)

        // A manager comparing List to String[] with equals would report this as not stored on every
        // apply — a permanently red device, which trains the reader to skip the one that means
        // something.
        assertTrue(outcome.toString(), outcome.ok)
        assertEquals(5, outcome.keys)
        assertEquals(1, outcome.blocked)
    }

    @Test
    fun `a key the platform accepted and did not store is reported`() {
        val gateway = FakeConfigGateway(drop = setOf(ChromePolicyManager.KEY_URL_BLOCKLIST))

        val outcome = ChromePolicyManager(gateway)
            .apply(listOf("example.com"), safeSearch = true, youtubeRestricted = true, neverBlocked = console)

        assertFalse(outcome.ok)
        assertTrue(
            outcome.missing.getValue(ChromePolicyManager.KEY_URL_BLOCKLIST).startsWith("expected "),
        )
    }

    @Test
    fun `a write that throws is a failure, and nothing claims to be applied`() {
        val gateway = FakeConfigGateway(throws = SecurityException("not the device owner"))

        val outcome = ChromePolicyManager(gateway)
            .apply(listOf("example.com"), safeSearch = true, youtubeRestricted = true, neverBlocked = console)

        assertFalse(outcome.ok)
        assertEquals("not the device owner", outcome.failure)
        assertTrue(outcome.missing.isEmpty())
    }

    @Test
    fun `the bundle is composed from scratch, never merged with what is stored`() {
        val gateway = FakeConfigGateway()
        val manager = ChromePolicyManager(gateway)

        manager.apply(listOf("a.example"), safeSearch = true, youtubeRestricted = true, neverBlocked = console)
        val outcome = manager.apply(emptyList(), safeSearch = false, youtubeRestricted = false, neverBlocked = console)

        assertTrue(outcome.toString(), outcome.ok)
        // FR-6.4 end to end: the first apply's blocklist is gone, not merged forward.
        val blocklist = gateway.current(ChromePolicyManager.CHROME_PACKAGE)[ChromePolicyManager.KEY_URL_BLOCKLIST]
        assertEquals(emptyList<String>(), (blocklist as Array<*>).toList())
        assertEquals(2, gateway.writes)
    }

    @Test
    fun `the policy key names are the ones Chrome publishes`() {
        // Pinned literally. A typo in one of these is silent in the worst way available: the bundle
        // stores and reads back perfectly, every test above stays green, and Chrome filters nothing
        // because it does not know the key. This test is a record of what was checked against
        // Chrome's enterprise policy list, not proof that the browser agrees.
        assertEquals("URLBlocklist", ChromePolicyManager.KEY_URL_BLOCKLIST)
        assertEquals("URLAllowlist", ChromePolicyManager.KEY_URL_ALLOWLIST)
        assertEquals("ForceGoogleSafeSearch", ChromePolicyManager.KEY_SAFE_SEARCH)
        assertEquals("ForceYouTubeRestrict", ChromePolicyManager.KEY_YOUTUBE_RESTRICT)
        assertEquals("IncognitoModeAvailability", ChromePolicyManager.KEY_INCOGNITO)
        assertEquals("com.android.chrome", ChromePolicyManager.CHROME_PACKAGE)
        // 2 is "strict", not "moderate"; 1 is "disabled", not "enabled". Both are values where the
        // neighbouring number is a plausible-looking policy that does much less.
        assertEquals(2, ChromePolicyManager.YOUTUBE_STRICT)
        assertEquals(0, ChromePolicyManager.YOUTUBE_OFF)
        assertEquals(1, ChromePolicyManager.INCOGNITO_DISABLED)
    }
}
