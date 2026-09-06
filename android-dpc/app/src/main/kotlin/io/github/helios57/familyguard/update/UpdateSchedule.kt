package io.github.helios57.familyguard.update

/**
 * Where [UpdateSchedule] keeps the instant the next check falls due.
 *
 * Stored rather than held in memory because the alternative is what the first version of FR-15.6
 * did: a coroutine holding its own countdown, which starts again from the beginning every time the
 * component holding it is restarted.
 */
interface UpdateScheduleStore {
    /** The wall-clock instant of the next check, or 0 when none has ever been booked. */
    fun dueAt(): Long

    fun setDueAt(atEpochMillis: Long)
}

/** An [UpdateScheduleStore] that forgets everything when the process ends. Tests, and nothing else. */
class InMemoryUpdateScheduleStore(private var due: Long = 0L) : UpdateScheduleStore {
    override fun dueAt(): Long = due

    override fun setDueAt(atEpochMillis: Long) {
        due = atEpochMillis
    }
}

/**
 * When the phone should next ask whether the server hosts a newer build (FR-15.6), decided on the
 * only clock that measures what a parent means by "fifteen minutes".
 *
 * **This class exists because the first version of FR-15.6 measured the wrong clock, and the
 * failure was silent.** The cadence was `delay(2 min)` and then `delay(15 min)`, in a coroutine
 * held by the connection loop. `kotlinx.coroutines.delay` schedules on a clock that stops while the
 * device is suspended — Android's `Handler` uses `SystemClock.uptimeMillis()`, the JVM's default
 * executor parks on `CLOCK_MONOTONIC`, and neither advances in deep sleep — and a phone in a pocket
 * is suspended almost all of the time.
 *
 * Measured on the pilot phone on 2026-09-06, out of the control plane's own request log: the
 * service was up and streaming for 38 minutes and made 40 heartbeats in that window, and made
 * **zero** `apk-info` requests. A two-minute delay had not elapsed in 38 minutes of wall clock.
 * The same log carries the control that makes it a clock problem rather than a dead coroutine:
 * [io.github.helios57.familyguard.net.EventStream]'s reconnect backoff, which is at most one second
 * after a clean close, took **426 seconds** — and finished ten seconds after an unrelated wake-up
 * had woken the CPU. Two different delays, one clock, one symptom.
 *
 * So the decision is made here on `System.currentTimeMillis()`, which does count while the phone
 * sleeps, and it is written down rather than carried in a coroutine. What wakes a sleeping phone to
 * make the decision is an `AlarmManager` wake-up that pierces doze — see
 * `io.github.helios57.familyguard.sync.AlarmManagerPlatform` — and every sync that happens for some
 * other reason also asks, which costs one comparison and means a phone that is already awake never
 * waits on the alarm.
 */
class UpdateSchedule(
    private val store: UpdateScheduleStore,
    private val now: () -> Long = System::currentTimeMillis,
) {

    /**
     * Establishes the next due instant when the connection comes up, and reports it.
     *
     * An instant already stored is kept, **including one in the past**: a phone that was switched
     * off across its check is due now and not two minutes from now, and a loop that restarts its own
     * first wait every time the connection settles is a phone that checks only if it is left
     * undisturbed longer than it ever is.
     *
     * The exception is an instant further out than anything this class books, which cannot have
     * been written by this class against the clock the phone has now. The clock moved — a child
     * setting it forward and back does exactly this, and so does a phone whose RTC starts at the
     * epoch and is corrected by the network — and the stored instant is then unreachable. It is
     * replaced rather than trusted.
     */
    fun arm(): Long {
        val stored = store.dueAt()
        if (stored != 0L && stored - now() <= RETRY_MILLIS) return stored
        return book(FIRST_CHECK_MILLIS)
    }

    /**
     * Whether a check is owed now.
     *
     * False before [arm] has ever run, which is before there is anything to owe: nothing has a
     * credential to check with until the connection is up.
     */
    fun isDue(): Boolean {
        val at = store.dueAt()
        return at != 0L && now() >= at
    }

    /** Books the ordinary next check, after one that ran. */
    fun checked(): Long = book(INTERVAL_MILLIS)

    /** Books the next check after a refusal, which waits far longer — see [RETRY_MILLIS]. */
    fun refused(): Long = book(RETRY_MILLIS)

    private fun book(inMillis: Long): Long {
        val at = now() + inMillis
        store.setDueAt(at)
        return at
    }

    companion object {
        /**
         * How long after the connection settles the first automatic update check runs.
         *
         * Not immediately. A phone coming back from a reboot has an enrollment, a policy, a
         * notification channel and an inventory to get through first, and an update that replaced
         * this app in the middle of that would restart every one of them. Two minutes is long
         * enough for that to be finished and short enough that a parent who has just deployed a
         * build does not conclude nothing happened.
         */
        const val FIRST_CHECK_MILLIS = 2 * 60 * 1000L

        /**
         * How often the phone asks whether the server hosts a newer build (FR-15.6).
         *
         * A new build becomes visible when the control plane restarts — it hashes the APK on the
         * node at startup — so this is the delay between a deploy and a phone taking it. The check
         * costs one small authenticated GET, and a phone already on the current build downloads
         * nothing: the comparison is made against the version the server declares.
         */
        const val INTERVAL_MILLIS = 15 * 60 * 1000L

        /**
         * How long the phone waits after a refused update before trying again.
         *
         * Long, deliberately. A refusal that is going to repeat — a signature that will never match,
         * an installer the platform will not run — repeats every time, and retrying it every quarter
         * of an hour costs a 13 MB download on a child's connection for each one. The reason is on
         * the console the whole time (FR-15.7), so the parent is not waiting on the retry to find
         * out something is wrong.
         *
         * It is also the furthest ahead this class ever books, which is what makes it the test for
         * a stored instant that the clock has moved out of reach — see [arm].
         */
        const val RETRY_MILLIS = 6 * 60 * 60 * 1000L
    }
}
