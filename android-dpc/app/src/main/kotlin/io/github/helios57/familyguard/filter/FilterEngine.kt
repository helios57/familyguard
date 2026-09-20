package io.github.helios57.familyguard.filter

import java.net.URI
import java.net.URISyntaxException
import java.util.concurrent.atomic.AtomicLong

/** What the filter decided about one name, and why. */
sealed interface Verdict {

    /** Let it through. [reason] is for the report, never for a decision. */
    data class Allow(val reason: AllowReason) : Verdict

    /** Refuse it: NXDOMAIN for a query, a reset for a connection. */
    data class Block(val host: String, val labels: Int) : Verdict
}

enum class AllowReason {
    /** The filter is switched off for this child, or no list has been compiled yet. */
    FILTER_OFF,

    /** The name is one this app may never block, whatever any list says. */
    NEVER_BLOCKED,

    /** A list rule said so — an `@@` exception, or one that outranks the block. */
    ALLOWED_BY_RULE,

    /** No rule mentions this name. */
    NO_RULE,
}

/** Running totals, for the parent-visible report. */
data class FilterCounters(
    val queriesSeen: Long,
    val queriesBlocked: Long,
    val connectionsSeen: Long,
    val connectionsBlocked: Long,
    val namesHidden: Long,
)

/**
 * The decision, separated from every way of reaching it (FR-6.6).
 *
 * DNS and SNI ask the same question about the same kind of name, so they share one engine and one
 * set of counters. What differs is only what the caller does with a [Verdict.Block] — synthesise an
 * NXDOMAIN, or reset the connection.
 *
 * ### The override that exists so this app can always be turned off again
 *
 * [neverBlocked] is checked **before** the rule index, and nothing in a list can outrank it. That is
 * deliberate and it is not a performance shortcut: if a downloaded list ever contained the control
 * plane's own hostname, the phone would stop syncing, and the only thing that can switch the filter
 * off is a sync. The device would be stuck filtering, with no way in, until someone took it apart
 * by hand. Putting the host in the index as an allow rule is *not* the same protection — a deeper
 * block rule beats a shallower allow by design (see [DomainIndex]), so `||sync.guard.example.com^`
 * would win over an allow on `guard.example.com`.
 *
 * ### Failing open
 *
 * An empty index allows everything, and so does [enabled] being false. There is no state of this
 * class that blocks by default. A filter that fails closed on a phone a child carries is a phone
 * with no internet and no explanation on the screen.
 */
class FilterEngine(
    initialIndex: DomainIndex = DomainIndex.EMPTY,
    neverBlocked: Collection<String> = emptyList(),
    initiallyEnabled: Boolean = false,
) {

    // Volatile rather than synchronised: the tunnel thread reads these on every packet and the sync
    // thread replaces them wholesale. Both are immutable values, so a reader sees either the old
    // list or the new one and never a half-built index.
    @Volatile
    private var index: DomainIndex = initialIndex

    @Volatile
    private var enabled: Boolean = initiallyEnabled

    /** Lowercased, and matched as suffixes, so a subdomain of a protected host is protected too. */
    private val protected: List<String> = neverBlocked
        .mapNotNull { RuleParser.normalise(it) }
        .distinct()

    private val queriesSeen = AtomicLong()
    private val queriesBlocked = AtomicLong()
    private val connectionsSeen = AtomicLong()
    private val connectionsBlocked = AtomicLong()
    private val namesHidden = AtomicLong()

    val ruleCount: Int get() = index.ruleCount

    fun isEnabled(): Boolean = enabled && index.ruleCount > 0

    /** Swap in a freshly compiled list. Safe to call while the tunnel is running. */
    fun update(index: DomainIndex, enabled: Boolean) {
        this.index = index
        this.enabled = enabled
    }

    /** A name asked for over DNS. */
    fun decideQuery(name: String): Verdict = decide(name).also {
        queriesSeen.incrementAndGet()
        if (it is Verdict.Block) queriesBlocked.incrementAndGet()
    }

    /**
     * A name read out of a TLS ClientHello.
     *
     * [echPresent] does not change the verdict — the public name is still the only name there is —
     * but it is counted, because it is the one way this filter goes quietly blind. When that counter
     * climbs, the answer is a list update, not a bug hunt.
     */
    fun decideConnection(name: String, echPresent: Boolean = false): Verdict = decide(name).also {
        connectionsSeen.incrementAndGet()
        if (it is Verdict.Block) connectionsBlocked.incrementAndGet()
        if (echPresent) namesHidden.incrementAndGet()
    }

    /** A connection whose name could not be read at all — never blocked, always counted. */
    fun connectionWithNoName() {
        connectionsSeen.incrementAndGet()
    }

    fun counters() = FilterCounters(
        queriesSeen = queriesSeen.get(),
        queriesBlocked = queriesBlocked.get(),
        connectionsSeen = connectionsSeen.get(),
        connectionsBlocked = connectionsBlocked.get(),
        namesHidden = namesHidden.get(),
    )

    private fun decide(rawName: String): Verdict {
        if (!enabled) return Verdict.Allow(AllowReason.FILTER_OFF)
        val name = RuleParser.normalise(rawName) ?: return Verdict.Allow(AllowReason.NO_RULE)
        if (isProtected(name)) return Verdict.Allow(AllowReason.NEVER_BLOCKED)

        val match = index.match(name) ?: return Verdict.Allow(AllowReason.NO_RULE)
        return if (match.allow) {
            Verdict.Allow(AllowReason.ALLOWED_BY_RULE)
        } else {
            Verdict.Block(name, match.labels)
        }
    }

    private fun isProtected(name: String): Boolean = protected.any { host ->
        // Suffix, on a label boundary. A bare `endsWith` would protect `evilguard.example.com`
        // because it ends with `guard.example.com`, which is how an allowlist becomes a hole.
        name == host || name.endsWith(".$host")
    }

    companion object {
        /**
         * The hosts this app must never filter: the control plane, and whatever the platform needs
         * to tell the device the time and that it is online.
         *
         * Derived from the server URL rather than configured, for the same reason
         * `ChromeApplier.allowlistFor` does it: a hand-maintained copy of the one string that must
         * be right is a copy that can be wrong.
         */
        fun neverBlockedFor(serverUrl: String): List<String> {
            val host = try {
                URI(serverUrl.trim()).host
            } catch (_: URISyntaxException) {
                null
            }
            return listOfNotNull(host?.takeIf { it.isNotBlank() }) + PLATFORM_ESSENTIALS
        }

        /**
         * Names the phone needs working whatever a list says.
         *
         * These are not conveniences. Connectivity check failures make Android show a permanent
         * "no internet" notification and can make Wi-Fi refuse to stay associated; a phone with no
         * time source fails TLS on everything, including the sync that would turn the filter off.
         */
        private val PLATFORM_ESSENTIALS = listOf(
            "connectivitycheck.gstatic.com",
            "connectivitycheck.android.com",
            "clients3.google.com",
            "android.clients.google.com",
            "time.android.com",
            "pool.ntp.org",
            "mtalk.google.com",
            "fcm.googleapis.com",
        )
    }
}
