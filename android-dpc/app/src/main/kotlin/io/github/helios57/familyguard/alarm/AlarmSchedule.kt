package io.github.helios57.familyguard.alarm

import java.time.DateTimeException
import java.time.Instant
import java.time.LocalTime
import java.time.ZoneId
import java.time.ZonedDateTime
import java.time.format.DateTimeParseException
import kotlinx.serialization.KSerializer
import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import kotlinx.serialization.descriptors.PrimitiveKind
import kotlinx.serialization.descriptors.PrimitiveSerialDescriptor
import kotlinx.serialization.encoding.Decoder
import kotlinx.serialization.encoding.Encoder

/**
 * A profile's alarm clock as the server sends it beside the policy (FR-23.1, FR-23.2): seven
 * weekdays, Monday first, `""` for a day that does not ring; the changes for single dates from today
 * on; and the profile's timezone, which is what the times are read in.
 *
 * The phone holds the rule rather than an instant, so it rings with no connection and computes the
 * next ring itself after a reboot, a change of timezone or a change of daylight saving time.
 */
@Serializable
data class AlarmSchedule(
    @SerialName("timezone") val timezone: String = "",
    @SerialName("weekdays") val weekdays: List<String> = List(7) { "" },
    @SerialName("overrides") val overrides: List<AlarmDayChange> = emptyList(),
)

/** A change for one calendar day: a time, or `null` for no alarm that day. */
@Serializable
data class AlarmDayChange(
    @SerialName("day") val day: String = "",
    @SerialName("time") val time: String? = null,
)

/** What the phone remembers between rings: a pending snooze, and the ring it last sounded. */
@Serializable
data class AlarmState(
    @Serializable(with = InstantAsEpochMillis::class) val snoozeUntil: Instant? = null,
    @Serializable(with = InstantAsEpochMillis::class) val lastRang: Instant? = null,
)

/** When the alarm rings next (FR-23.3). Pure: the clock and the rule are both arguments. */
object NextAlarm {

    /** Far enough to reach every date change the server can send (60 days) and a week past it. */
    private const val HORIZON_DAYS = 68L

    /**
     * The first ring strictly after [now], or null when there is none — including when the zone or
     * every time is unreadable: an alarm computed from a guess is worse than a phone that says it has
     * none, because the child trusts the one on the screen.
     *
     * A time that does not exist on a spring-forward day rings when the clock resumes (java.time's
     * rule for a gap); a time that happens twice on a fall-back day rings the first time only, since
     * the second is not strictly after the first once that has rung.
     */
    fun next(schedule: AlarmSchedule, now: Instant): Instant? {
        val zone = try {
            ZoneId.of(schedule.timezone)
        } catch (e: DateTimeException) {
            return null
        }
        val changes = schedule.overrides.associate { it.day to it.time }
        val today = now.atZone(zone).toLocalDate()
        for (offset in 0..HORIZON_DAYS) {
            val date = today.plusDays(offset)
            val key = date.toString()
            val time = if (changes.containsKey(key)) changes[key] else schedule.weekdays.getOrNull(date.dayOfWeek.value - 1)
            val local = parse(time) ?: continue
            val ring = ZonedDateTime.ofLocal(date.atTime(local), zone, null).toInstant()
            if (ring.isAfter(now)) return ring
        }
        return null
    }

    private fun parse(time: String?): LocalTime? {
        if (time.isNullOrEmpty() || !CLOCK.matches(time)) return null
        return try {
            LocalTime.parse(time)
        } catch (e: DateTimeParseException) {
            null
        }
    }

    private val CLOCK = Regex("^([01][0-9]|2[0-3]):[0-5][0-9]$")

}

/** The Heute screen's first line (FR-23.5): *Wecker: heute 06:30*, *morgen 06:30* or *Fr 07:00*. */
data class AlarmLine(val day: Day, val time: String, val weekday: java.time.DayOfWeek) {
    enum class Day { TODAY, TOMORROW, LATER }

    companion object {
        fun of(schedule: AlarmSchedule, ring: Instant, now: Instant): AlarmLine? {
            val zone = try {
                ZoneId.of(schedule.timezone)
            } catch (e: DateTimeException) {
                return null
            }
            val at = ring.atZone(zone)
            val today = now.atZone(zone).toLocalDate()
            val day = when (at.toLocalDate()) {
                today -> Day.TODAY
                today.plusDays(1) -> Day.TOMORROW
                else -> Day.LATER
            }
            return AlarmLine(day, at.toLocalTime().toString(), at.dayOfWeek)
        }
    }
}

/** What to book: the earlier of a pending snooze and the next scheduled ring not yet rung. */
object AlarmBooking {
    fun next(schedule: AlarmSchedule, now: Instant, state: AlarmState): Instant? {
        // Computed from the later of now and the last ring, so a ring that fired a moment early by
        // the wall clock is not booked a second time.
        val from = state.lastRang?.takeIf { it.isAfter(now) } ?: now
        val scheduled = NextAlarm.next(schedule, from)
        val snooze = state.snoozeUntil?.takeIf { it.isAfter(now) }
        return listOfNotNull(scheduled, snooze).minOrNull()
    }
}

/**
 * The alarm stream's level while the alarm rings: at least half the stream, so a child cannot mute
 * an alarm clock by sliding it to zero, and never lowered. Null means leave it as it is.
 */
object AlarmVolume {
    fun ringLevel(current: Int?, max: Int): Int? {
        val floor = max / 2
        return if (current == null || current < floor) floor else null
    }
}

/** Instants as epoch milliseconds in the alarm's own state file. */
object InstantAsEpochMillis : KSerializer<Instant> {
    override val descriptor = PrimitiveSerialDescriptor("InstantAsEpochMillis", PrimitiveKind.LONG)
    override fun serialize(encoder: Encoder, value: Instant) = encoder.encodeLong(value.toEpochMilli())
    override fun deserialize(decoder: Decoder): Instant = Instant.ofEpochMilli(decoder.decodeLong())
}
