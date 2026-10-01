package io.github.helios57.familyguard.alarm

import java.io.File
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * The alarm receivers do their disk and keystore work off the main thread (FR-23.3, FR-23.4).
 *
 * `AlarmClock.fired` and `rebook` open EncryptedSharedPreferences — a keystore round trip and a
 * synchronous `commit` — and then call AlarmManager over binder. `onReceive` runs on the main
 * thread, which at the moment an alarm fires is also the thread [AlarmRingService] and
 * [AlarmActivity] need to start ringing and draw over the lock screen; a slow keystore there is an
 * alarm that rings late, and past the receiver's budget it is an ANR at 06:30.
 *
 * There is no Robolectric here, so a receiver cannot be run on the JVM. The source is the evidence,
 * and the reader is calibrated on the exact shape the receivers had before it is believed.
 */
class AlarmReceiversOffMainThreadTest {

    private val main: File = sequenceOf("src/main", "app/src/main", "android-dpc/app/src/main")
        .map(::File)
        .firstOrNull { it.isDirectory }
        ?: throw AssertionError(
            "could not find the main source set from ${File(".").absolutePath}; this test would " +
                "otherwise pass by scanning nothing",
        )

    private val alarm = File(main, "kotlin/io/github/helios57/familyguard/alarm")

    /** Source with comments removed, so prose naming a call is not read as the call. */
    private fun code(text: String): String = text
        .replace(Regex("""/\*.*?\*/""", RegexOption.DOT_MATCHES_ALL), "")
        .replace(Regex("""(?<!:)//[^\n]*"""), "")

    @Test
    fun `every alarm receiver hands its AlarmClock work to a background thread`() {
        // Calibration: the receivers as they were until 2026-10-01, both calls on the main thread.
        val before = """
            class AlarmFireReceiver : BroadcastReceiver() {
                override fun onReceive(context: Context, intent: Intent) {
                    if (intent.action != AlarmClock.ACTION_FIRE) return
                    AlarmRingService.start(context)
                    AlarmClock.fired(context, at)
                }
            }
            class TimeChangeReceiver : BroadcastReceiver() {
                override fun onReceive(context: Context, intent: Intent) {
                    when (intent.action) {
                        Intent.ACTION_TIME_CHANGED -> AlarmClock.rebook(context)
                    }
                }
            }
        """
        assertEquals(
            "the reader does not see AlarmClock work on the main thread, so its answer on the real " +
                "files means nothing",
            listOf("AlarmFireReceiver: AlarmClock.fired(", "TimeChangeReceiver: AlarmClock.rebook("),
            onTheMainThread(before),
        )

        val files = alarm.listFiles { f -> f.extension == "kt" }.orEmpty()
        val sources = files.associate { it.name to code(it.readText()) }
        val receivers = sources.values.flatMap { receiversIn(it).keys }
        assertTrue(
            "only $receivers were found under ${alarm.path}; the fire and the time-change receivers " +
                "make two, so the scan is reading the wrong place",
            receivers.containsAll(listOf("AlarmFireReceiver", "TimeChangeReceiver")),
        )

        assertEquals(
            "an alarm receiver does AlarmClock's disk and keystore work on the main thread",
            emptyList<String>(),
            sources.values.flatMap { onTheMainThread(it) },
        )
    }

    @Test
    fun `the background helper holds the broadcast open until the work is done`() {
        val text = code(File(alarm, "AlarmReceivers.kt").readText())
        val helper = Regex("""fun BroadcastReceiver\.offMainThread\(""").find(text)
            ?: throw AssertionError("AlarmReceivers.kt has no offMainThread helper any more")
        val body = blockFrom(text, helper.range.last)
        assertTrue("the helper does not call goAsync(), so the process may be killed mid-write", "goAsync()" in body)
        assertTrue(
            "the helper does not finish() the pending result in a finally, so one failure leaves the " +
                "broadcast open until the platform kills it",
            Regex("""finally\s*\{[^}]*\.finish\(\)""").containsMatchIn(body),
        )
        assertTrue("the helper starts no thread, so the work still runs where it was called", "thread(" in body)
    }

    @Test
    fun `the fire receiver still starts the ring`() {
        val fire = receiversIn(code(File(alarm, "AlarmReceivers.kt").readText()))["AlarmFireReceiver"]
            ?: throw AssertionError("AlarmFireReceiver is not in AlarmReceivers.kt any more")
        assertTrue("AlarmFireReceiver no longer starts AlarmRingService", "AlarmRingService.start(" in fire)
    }

    /**
     * Every entry point of [AlarmClock] is serialised.
     *
     * Each one is read-decide-book: read the rule and the state, compute the next ring, hand it to
     * AlarmManager, which replaces the booking. Its callers are on four threads — the fire and
     * time-change receivers' own, the sync's IO thread (a new rule), the ring service's main thread
     * (stop, snooze) — and two interleaved can land in the wrong order: an `update` that read the
     * state just before `fired` recorded the ring books that same minute again after `fired` has
     * booked tomorrow, and the alarm rings twice.
     */
    @Test
    fun `every AlarmClock entry point is synchronized`() {
        val before = """
            object AlarmClock {
                fun rebook(context: Context, now: Instant = Instant.now()): Instant? { return null }
                @Synchronized
                fun fired(context: Context, at: Instant) { }
                private fun firePendingIntent(context: Context, at: Instant?): PendingIntent = TODO()
            }
        """
        assertEquals(
            "the reader does not single out the unsynchronized entry point",
            listOf("rebook"),
            unsynchronized(before),
        )

        val text = code(File(alarm, "AlarmClock.kt").readText())
        val entryPoints = Regex("""(?m)^\s*fun (\w+)\(""").findAll(text).map { it.groupValues[1] }.toList()
        assertTrue(
            "AlarmClock declares $entryPoints; fired, rebook, update, snooze and stopped make five, " +
                "so the scan is reading the wrong shape",
            entryPoints.containsAll(listOf("fired", "rebook", "update", "snooze", "stopped")),
        )
        assertEquals(
            "an AlarmClock entry point is not @Synchronized; two callers can book out of order",
            emptyList<String>(),
            unsynchronized(text),
        )
    }

    /** The names of the non-private functions in [code] that are not annotated @Synchronized. */
    private fun unsynchronized(code: String): List<String> =
        Regex("""(?m)^[ \t]*(@Synchronized\s+)?fun (\w+)\(""").findAll(code)
            .filter { it.groupValues[1].isEmpty() }
            .map { it.groupValues[2] }
            .toList()

    /** "Receiver: call" for every AlarmClock function called in an onReceive outside offMainThread. */
    private fun onTheMainThread(code: String): List<String> =
        receiversIn(code).flatMap { (name, onReceive) ->
            val background = Regex("""offMainThread\(""").findAll(onReceive)
                .map { m -> onReceive.indexOf('{', m.range.last).let { open -> open..(open + blockFrom(onReceive, open).length) } }
                .toList()
            Regex("""AlarmClock\.[a-z]\w*\(""").findAll(onReceive)
                .filter { m -> background.none { m.range.first in it } }
                .map { "$name: ${it.value}" }
                .toList()
        }

    /** Each `class X : BroadcastReceiver()` in [code], mapped to the body of its onReceive. */
    private fun receiversIn(code: String): Map<String, String> =
        Regex("""class (\w+)\s*:\s*BroadcastReceiver\(\)""").findAll(code).associate { m ->
            val body = blockFrom(code, m.range.last)
            val on = Regex("""override fun onReceive\(""").find(body)
                ?: throw AssertionError("${m.groupValues[1]} has no onReceive; the reader is lost")
            m.groupValues[1] to blockFrom(body, on.range.last)
        }

    /** The text of the first `{ … }` block at or after [from], braces included. */
    private fun blockFrom(code: String, from: Int): String {
        val open = code.indexOf('{', from)
        if (open < 0) throw AssertionError("no block after offset $from; the reader is lost")
        var depth = 0
        for (i in open until code.length) {
            if (code[i] == '{') depth++
            if (code[i] == '}' && --depth == 0) return code.substring(open, i + 1)
        }
        throw AssertionError("the braces from offset $open never balanced; the reader is lost")
    }
}
