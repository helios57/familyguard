package io.github.helios57.familyguard.plan

import io.github.helios57.familyguard.enforce.EnforcementEngine
import io.github.helios57.familyguard.enforce.TodayReport

/**
 * The child's time today as three kinds, decided without a view (FR-28.3) — the owner's words of
 * 2026-10-01: "bonus time stays a week, additional time stays till midnight, daily time is reset at
 * midnight".
 *
 *  - **Tageszeit** — the daily limit; it starts over at midnight.
 *  - **Extrazeit** — a parent's minutes for today (by hand, or as the answer to a request); it ends at
 *    midnight.
 *  - **Bonuszeit** — earned with tasks; it stays seven days and is spent after the other two.
 */
object TimeView {

    /** What the time card shows. [leftMinutes] is null when there is no daily limit. */
    data class Card(
        val usedMinutes: Int,
        val leftMinutes: Int?,
        val dailyMinutes: Int,
        val extraMinutes: Int,
        val bonusMinutes: Int,
        /** The credit with minutes left that runs out first, or null. */
        val bonusExpiry: DayCredit?,
    )

    fun card(report: TodayReport, plan: DayPlan?): Card {
        val bonus = report.earnedMinutesLeft
        val left = if (report.dailyLimitMinutes > 0) {
            maxOf(0, report.limitMinutes - report.usedMinutes) + maxOf(0, bonus)
        } else {
            null
        }
        return Card(
            usedMinutes = report.usedMinutes,
            leftMinutes = left,
            dailyMinutes = report.dailyLimitMinutes,
            extraMinutes = report.bonusMinutes,
            bonusMinutes = bonus,
            bonusExpiry = plan?.let { DayPlanView.soonestExpiry(it.earned.credits) }?.takeIf { bonus > 0 },
        )
    }

    /** What the "Mehr Zeit erbitten" place says and offers. */
    enum class Ask {
        /** No button: no daily limit (nothing to ask for), paused or bedtime (Extrazeit would not open anything). */
        HIDDEN,
        OFFER,
        /** A request is waiting for an answer. */
        WAITING,
        /** Today's requests are used up. */
        NONE_LEFT,
    }

    fun ask(report: TodayReport?, plan: DayPlan?): Ask {
        if (report == null || report.dailyLimitMinutes <= 0) return Ask.HIDDEN
        if (report.suspendReason == EnforcementEngine.REASON_PAUSED || report.suspendReason == EnforcementEngine.REASON_BEDTIME) {
            return Ask.HIDDEN
        }
        val latest = latest(plan)
        if (latest?.state == DayTimeRequest.OPEN) return Ask.WAITING
        if (plan?.timeRequestsLeft == 0) return Ask.NONE_LEFT
        return Ask.OFFER
    }

    /** Today's newest request, or null — only of the plan's own day, which the server already filters. */
    fun latest(plan: DayPlan?): DayTimeRequest? = plan?.timeRequests?.firstOrNull()

    /**
     * A request that was waiting in [before] and is answered in [after]: what the phone tells the
     * child about with a notification. Null for anything else — a request the phone never saw
     * waiting is not news, and an answer seen once is not told twice.
     */
    fun newlyAnswered(before: DayPlan?, after: DayPlan): DayTimeRequest? {
        val waiting = before?.timeRequests.orEmpty().filter { it.state == DayTimeRequest.OPEN }.map { it.id }.toSet()
        return after.timeRequests.firstOrNull { it.id in waiting && it.state != DayTimeRequest.OPEN }
    }
}
