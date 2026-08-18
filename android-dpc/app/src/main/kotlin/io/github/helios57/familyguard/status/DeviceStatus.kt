package io.github.helios57.familyguard.status

import java.util.concurrent.TimeUnit

/**
 * How one line reads — and, the part this enum exists for, how it must never read.
 *
 * [NOT_MEASURED] is a third state, not a flavour of [ATTENTION], for the reason this whole codebase
 * keeps running into: a control that reports zero having evaluated nothing is indistinguishable from
 * one that evaluated everything and found nothing wrong. "0 minutes of screen time today" and
 * "screen time cannot be measured on this phone" render identically the moment they share a level,
 * and the second is the one that means the daily quota will never be reached.
 */
enum class StatusLevel {
    /** Measured, and as it should be. */
    OK,

    /** Measured, and something a parent should do something about. */
    ATTENTION,

    /** Not measured. The line says why, and it is never counted as OK. */
    NOT_MEASURED,
}

/** One labelled fact on the status screen. */
data class StatusLine(val label: String, val value: String, val level: StatusLevel)

/**
 * Everything the on-device status screen shows (FR-13.4), as data rather than as views.
 *
 * The whole point of this type is that it can be built and asserted on the JVM. A status screen
 * assembled inside an `Activity` is one that can only be checked by looking at it, and looking at it
 * is exactly how a screen that reports a healthy phone while measuring nothing survives.
 */
data class DeviceStatus(val lines: List<StatusLine>) {

    /** The line with this label, or an error naming the labels that do exist. */
    fun line(label: String): StatusLine =
        lines.firstOrNull { it.label == label }
            ?: throw NoSuchElementException(
                "no status line labelled \"$label\"; the screen has ${lines.map { it.label }}"
            )

    /** True if anything is wrong *or* unmeasurable. Both put the summary banner on the screen. */
    fun needsAttention(): Boolean = lines.any { it.level != StatusLevel.OK }

    /** The lines that are not OK, in the order they are shown. What the banner summarises. */
    fun problems(): List<StatusLine> = lines.filter { it.level != StatusLevel.OK }
}

/**
 * The facts the screen is built from, gathered once, off the main thread.
 *
 * **Every fact that can be unknown is nullable, and null means "not measured" everywhere.** That is
 * the invariant the whole file turns on: a reader that cannot see screen time returns null, not 0,
 * and [deviceStatus] renders null as a [StatusLevel.NOT_MEASURED] line carrying the reason. There is
 * no path in this file that turns an absence into a number.
 *
 * There is no device *token* here, and there must never be one. The screen is reachable by anyone
 * holding the phone — it is the launcher entry (see `RecoveryActivity`) — so anything on it is
 * public to the child. The device id is not a secret and the parent needs it to match the phone to
 * the console; the token would let anyone who read the screen impersonate the device to the server.
 * `DeviceStatusTest` asserts the absence rather than trusting this paragraph.
 */
data class StatusFacts(
    /** Null on a device that has never enrolled. */
    val deviceId: String?,
    /** The host the device syncs with, for the parent to check they are looking at the right one. */
    val serverHost: String?,
    /** Null when it could not be determined — an unusual state, and not the same as `false`. */
    val deviceOwner: Boolean?,
    /** When a recovery code released this phone, or null if it is under management. */
    val releasedSinceMillis: Long?,
    /** The version this device last applied cleanly. 0 when it never has. */
    val appliedPolicyVersion: Long,
    /** The version the server last sent, or null if nothing is cached. */
    val cachedPolicyVersion: Long?,
    /** When the server was last reached. 0 means never. */
    val lastServerContactMillis: Long,
    /** Screen time counted today, or **null when it cannot be measured at all**. */
    val screenTimeTodayMillis: Long?,
    /** Why [screenTimeTodayMillis] is null. Shown verbatim; it names the missing permission. */
    val screenTimeUnavailableReason: String,
    /** The daily limit in minutes, 0 when the parent has not set one. */
    val quotaMinutes: Int,
    /** Recovery attempts made on this phone that the server has not been told about yet. */
    val unreportedRecoveryAttempts: Int,
    val nowMillis: Long,
)

/** The labels, in one place, so the tests and the screen cannot drift apart. */
object StatusLabels {
    const val ENROLLMENT = "Enrollment"
    const val DEVICE_OWNER = "Management"
    const val RULES = "Rules"
    const val POLICY = "Settings version"
    const val LAST_CONTACT = "Last reached the family settings"
    const val SCREEN_TIME = "Screen time today"
    const val UNREPORTED = "Waiting to be reported"
}

/**
 * Composes the status a parent reads off the phone.
 *
 * Pure: same facts in, same lines out, no clock of its own and no Android import. Everything that
 * touches the device is in `AndroidDeviceStatus.kt`, and the split is what lets the eleven cases
 * below be asserted in milliseconds instead of on a handset.
 */
fun deviceStatus(facts: StatusFacts): DeviceStatus {
    val lines = mutableListOf<StatusLine>()

    lines += if (facts.deviceId == null) {
        StatusLine(
            StatusLabels.ENROLLMENT,
            "not set up yet — scan the QR code from the family settings",
            StatusLevel.ATTENTION,
        )
    } else {
        StatusLine(
            StatusLabels.ENROLLMENT,
            "set up as ${facts.deviceId}" + (facts.serverHost?.let { " with $it" } ?: ""),
            StatusLevel.OK,
        )
    }

    lines += when (facts.deviceOwner) {
        // Not a rephrasing of `false`. "We asked and the answer was no" and "we could not ask" are
        // different problems with different fixes, and one of them is a bug in this app.
        null -> StatusLine(
            StatusLabels.DEVICE_OWNER,
            "could not be determined on this phone",
            StatusLevel.NOT_MEASURED,
        )

        false -> StatusLine(
            StatusLabels.DEVICE_OWNER,
            "this app is not this phone's device owner, so no rule can be applied",
            StatusLevel.ATTENTION,
        )

        true -> StatusLine(StatusLabels.DEVICE_OWNER, "this app manages this phone", StatusLevel.OK)
    }

    lines += if (facts.releasedSinceMillis != null) {
        StatusLine(
            StatusLabels.RULES,
            "off since ${ago(facts.nowMillis - facts.releasedSinceMillis)} — a recovery code was " +
                "used. They come back when this phone next reaches the family settings.",
            StatusLevel.ATTENTION,
        )
    } else {
        StatusLine(StatusLabels.RULES, "on", StatusLevel.OK)
    }

    lines += when {
        facts.appliedPolicyVersion == 0L && facts.cachedPolicyVersion == null ->
            StatusLine(StatusLabels.POLICY, "no settings have reached this phone yet", StatusLevel.ATTENTION)

        // The gap that the console cannot see on its own: the server sent v9, this phone is still
        // enforcing v7, and every rule added in between is simply not happening.
        facts.cachedPolicyVersion != null && facts.cachedPolicyVersion > facts.appliedPolicyVersion ->
            StatusLine(
                StatusLabels.POLICY,
                "version ${facts.cachedPolicyVersion} was sent, but this phone is still applying " +
                    "version ${facts.appliedPolicyVersion}",
                StatusLevel.ATTENTION,
            )

        else -> StatusLine(StatusLabels.POLICY, "version ${facts.appliedPolicyVersion}", StatusLevel.OK)
    }

    lines += if (facts.lastServerContactMillis <= 0) {
        StatusLine(StatusLabels.LAST_CONTACT, "never", StatusLevel.ATTENTION)
    } else {
        val age = facts.nowMillis - facts.lastServerContactMillis
        StatusLine(
            StatusLabels.LAST_CONTACT,
            ago(age),
            // A phone enforcing a week-old policy is still enforcing, so this is not a failure —
            // but it is the first thing to look at when a change the parent made has not happened.
            if (age >= STALE_CONTACT_MILLIS) StatusLevel.ATTENTION else StatusLevel.OK,
        )
    }

    lines += if (facts.screenTimeTodayMillis == null) {
        // The line this whole file exists for. Without it a phone that cannot see usage reports
        // zero minutes, the daily limit is never reached, and the console shows a child who spent
        // the day off their phone — which a parent has no way to tell from the real thing.
        StatusLine(StatusLabels.SCREEN_TIME, facts.screenTimeUnavailableReason, StatusLevel.NOT_MEASURED)
    } else {
        val minutes = TimeUnit.MILLISECONDS.toMinutes(facts.screenTimeTodayMillis).toInt()
        StatusLine(
            StatusLabels.SCREEN_TIME,
            if (facts.quotaMinutes > 0) "$minutes of ${facts.quotaMinutes} minutes" else "$minutes minutes",
            StatusLevel.OK,
        )
    }

    if (facts.unreportedRecoveryAttempts > 0) {
        lines += StatusLine(
            StatusLabels.UNREPORTED,
            "${facts.unreportedRecoveryAttempts} recovery " +
                (if (facts.unreportedRecoveryAttempts == 1) "attempt" else "attempts") +
                " this phone has not been able to report yet",
            StatusLevel.ATTENTION,
        )
    }

    return DeviceStatus(lines)
}

/** Anything older than this makes the contact line worth looking at. */
private const val STALE_CONTACT_MILLIS = 24L * 60 * 60 * 1000

/**
 * A rough age, in the largest unit that is still honest.
 *
 * Rounded *down*, and never below "just now": this is read by somebody deciding whether the phone is
 * current, and rounding an eleven-hour-old contact up to "1 day ago" is a screen that overstates the
 * problem, while rounding 90 minutes down to "1 hour ago" understates nothing that matters. A
 * negative age — a clock that moved — reads as "just now" rather than as a time in the future.
 */
internal fun ago(millis: Long): String {
    val seconds = millis / 1000
    return when {
        seconds < 60 -> "just now"
        seconds < 3600 -> plural(seconds / 60, "minute") + " ago"
        seconds < 86_400 -> plural(seconds / 3600, "hour") + " ago"
        else -> plural(seconds / 86_400, "day") + " ago"
    }
}

private fun plural(count: Long, unit: String): String = "$count $unit" + if (count == 1L) "" else "s"
