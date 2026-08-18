package io.github.helios57.familyguard.usage

/** Where the cumulative day totals survive a process death. */
interface UsageStore {
    fun load(): Map<String, Map<String, Long>>
    fun save(totals: Map<String, Map<String, Long>>)
}

/** A [UsageStore] that forgets everything when the process ends. Tests, and nothing else. */
class InMemoryUsageStore(initial: Map<String, Map<String, Long>> = emptyMap()) : UsageStore {
    private var state: Map<String, Map<String, Long>> = initial
    override fun load(): Map<String, Map<String, Long>> = state
    override fun save(totals: Map<String, Map<String, Long>>) {
        state = totals
    }
}

/**
 * The running per-day, per-package foreground totals this device has measured.
 *
 * **Cumulative, never deltas.** The server upserts with `GREATEST(stored, reported)`, so a report
 * that arrives twice, out of order, or after a retry can never lower a total — but only if what is
 * sent is the whole day's figure. A device sending increments through that same upsert would have
 * every duplicate silently discarded and every gap silently kept.
 *
 * That also decides what has to be persisted. A phone that reboots at 19:00 and starts counting from
 * zero would report totals below what the server already holds, `GREATEST` would keep the old ones,
 * and every minute of the evening would be invisible to the quota until the fresh counter overtook
 * the morning's. So the totals live in storage, not in this object.
 */
class UsageLedger(
    private val store: UsageStore,
    /**
     * How many days of history to keep. The server refuses a day more than a week old, so anything
     * older can never be delivered — and a map that only grows is state on a device that may not
     * reach the network for months.
     */
    private val retainDays: Int = 7,
) {

    private val totals: MutableMap<String, MutableMap<String, Long>> =
        store.load().mapValuesTo(sortedMapOf()) { (_, packages) -> LinkedHashMap(packages) }

    /**
     * Adds one window's measurement, credited to the days it actually spans.
     *
     * [budgetMillis] is how much real time the monotonic clock says passed with the screen on since
     * the previous call. It is a ceiling, and it is the whole of FR-3.2 and FR-3.3 in one number:
     *
     * - A wall clock pushed forward makes the platform's own event timestamps span more time than
     *   really elapsed. Without the ceiling that arrives as usage, and moving the clock forward
     *   repeatedly would burn a child's daily quota — or, moved the other way by an adult
     *   troubleshooting something, would hand back hours that were already spent.
     * - Time with the screen off is not in the budget at all, so it cannot be credited to any
     *   package however the platform reports it.
     *
     * When the attributed total exceeds the budget every package is scaled down in proportion rather
     * than truncated at an arbitrary one, because which package gets cut would otherwise depend on
     * map iteration order.
     *
     * @return the days whose totals changed.
     */
    fun add(byDay: Map<String, Map<String, Long>>, budgetMillis: Long): Set<String> {
        if (budgetMillis <= 0) return emptySet()
        val measured = byDay.values.sumOf { packages -> packages.values.sumOf { it.coerceAtLeast(0) } }
        if (measured <= 0) return emptySet()

        val changed = linkedSetOf<String>()
        for ((day, packages) in byDay) {
            for ((pkg, raw) in packages) {
                if (pkg.isBlank() || raw <= 0) continue
                // Integer arithmetic, floored: a package can lose up to a millisecond to the scale,
                // which is three orders of magnitude below anything a quota is expressed in.
                val credited = if (measured > budgetMillis) raw * budgetMillis / measured else raw
                if (credited <= 0) continue
                val bucket = totals.getOrPut(day) { linkedMapOf() }
                bucket[pkg] = (bucket[pkg] ?: 0L) + credited
                changed += day
            }
        }
        if (changed.isNotEmpty()) prune()
        if (changed.isNotEmpty()) store.save(snapshot())
        return changed
    }

    /** The cumulative total for one day, in milliseconds per package. Empty for a day never seen. */
    fun totals(day: String): Map<String, Long> = totals[day]?.toMap() ?: emptyMap()

    /** Every day this device is still holding, oldest first. */
    fun days(): List<String> = totals.keys.sorted()

    /** Forgets everything. Only an un-enrollment should reach this. */
    fun clear() {
        totals.clear()
        store.save(emptyMap())
    }

    private fun prune() {
        if (totals.size <= retainDays) return
        // ISO day keys sort lexicographically in calendar order, which is the one property this
        // relies on — `2026-09-01` after `2026-08-31` without parsing either.
        val keep = totals.keys.sorted().takeLast(retainDays).toSet()
        totals.keys.retainAll(keep)
    }

    private fun snapshot(): Map<String, Map<String, Long>> =
        totals.mapValues { (_, packages) -> packages.toMap() }
}
