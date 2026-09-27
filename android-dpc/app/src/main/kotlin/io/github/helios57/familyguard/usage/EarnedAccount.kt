package io.github.helios57.familyguard.usage

import android.content.Context
import io.github.helios57.familyguard.enforce.EarnedAttribution
import io.github.helios57.familyguard.enforce.EarnedContext

/**
 * The earned time (Bonuszeit) this device has spent, per day and package (FR-22).
 *
 * A second [UsageLedger] beside the day totals, in its own file, cumulative like them and reported
 * with them — so the server merges it with `GREATEST` the same way and a lost report is repaired by
 * the next. Every figure in it is a part of a figure in the day totals: the minutes a window's
 * attribution said were paid from earned time.
 */
class EarnedAccount(val ledger: UsageLedger) {

    /** The context of the state last enforced; null until the engine has run once. */
    @Volatile
    var context: EarnedContext? = null

    /** Charges one credited window. A window on a day the context is not for charges only bonus apps. */
    fun charge(credited: Map<String, Map<String, Long>>) {
        val ctx = context ?: return
        for ((day, packages) in credited) {
            val gold = if (day == ctx.day) {
                EarnedAttribution.attribute(packages, ctx)
            } else {
                packages.filterKeys { it in ctx.bonus }
            }
            if (gold.isNotEmpty()) ledger.add(mapOf(day to gold), Long.MAX_VALUE)
        }
    }

    /**
     * Today's count, split: the minutes that were use against the budget, and the minutes paid from
     * earned time. [counted] is the day total already filtered of uncounted packages.
     */
    fun split(day: String, counted: Map<String, Long>, uncounted: Set<String>): Pair<Int, Int> {
        val gold = ledger.totals(day).filterKeys { it !in uncounted }.values.sum()
        val total = counted.values.sum()
        return ((total - gold).coerceAtLeast(0) / 60_000L).toInt() to (gold / 60_000L).toInt()
    }

    companion object {
        const val FILE = "family-guard-earned"

        fun open(context: Context) = EarnedAccount(UsageLedger(EncryptedUsageStore(context, FILE)))
    }
}
