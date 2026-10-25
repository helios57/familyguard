package io.github.helios57.familyguard.usage

import android.content.Context

/**
 * What the engine needs to know to count a window as learning (FR-30.3): the day it enforced, the
 * learning packages it resolved, and whether it was enforcing at all — a paused or watch-only phone
 * earns nothing, so a child cannot bank time while nothing is being limited.
 */
data class LearningContext(val day: String, val learning: Set<String>, val enforcing: Boolean)

/**
 * The time this device spent in learning apps, per day and package (FR-30.3).
 *
 * A third [UsageLedger] beside the day totals and [EarnedAccount], in its own file, cumulative like
 * them and reported with them as `learning` — so the server merges it with `GREATEST` and a lost
 * report is repaired by the next. Every figure is a part of a figure in the day totals.
 */
class LearningAccount(val ledger: UsageLedger) {

    /** The context of the state last enforced; null until the engine has run once. */
    @Volatile
    var context: LearningContext? = null

    /**
     * Credits one window. Only today's learning packages count, and only while enforcing: a window on
     * a day the context is not for is left out rather than guessed at.
     */
    fun credit(credited: Map<String, Map<String, Long>>) {
        val ctx = context ?: return
        if (!ctx.enforcing) return
        val learned = credited[ctx.day]?.filterKeys { it in ctx.learning }.orEmpty().filterValues { it > 0 }
        if (learned.isNotEmpty()) ledger.add(mapOf(ctx.day to learned), Long.MAX_VALUE)
    }

    /** The minutes learned on [day]: the day's sum floored, not each app's — as the server floors it. */
    fun minutesToday(day: String): Int = (ledger.totals(day).values.sum() / 60_000L).toInt()

    companion object {
        const val FILE = "family-guard-learning"

        fun open(context: Context) = LearningAccount(UsageLedger(EncryptedUsageStore(context, FILE)))
    }
}
