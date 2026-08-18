package io.github.helios57.familyguard.commands

/**
 * One position, and the instant it was actually taken.
 *
 * [capturedAtEpochMillis] is the fix's own timestamp, never the moment it was read. The two differ
 * by minutes or hours whenever the answer came from the platform's cache, and that difference is
 * the whole value of the field — a parent looking at a pin on a map is entitled to know whether it
 * says where the phone is or where it was before school.
 */
data class Fix(
    val latitude: Double,
    val longitude: Double,
    val accuracyM: Double?,
    val capturedAtEpochMillis: Long,
)

/** The three things locating needs from the platform, named so the decision is testable. */
interface LocationSource {
    /**
     * Whether this app holds a location permission right now.
     *
     * Asked rather than inferred from a failed fix: "the permission was revoked" is something a
     * parent can fix and "the phone is in a basement" is not, and both otherwise arrive as null.
     */
    fun permitted(): Boolean

    /**
     * Blocks for at most [timeoutMillis] waiting for a new fix, then releases the hardware.
     *
     * Null means none arrived. It is not an error: indoors, in a lift, or in a tunnel is the normal
     * case for the phone a parent is trying to find.
     */
    fun freshFix(timeoutMillis: Long): Fix?

    /** The platform's last known position, carrying its own instant. Null when there is none. */
    fun lastKnownFix(): Fix?
}

/** What one `LOCATE_NOW` produced. */
sealed interface ProbeResult {
    /**
     * @param fresh whether this came from the hardware just now or from the platform's cache.
     * @param ageMillis how old the fix is, by this device's clock. Zero for a fresh one.
     */
    data class Located(val fix: Fix, val fresh: Boolean, val ageMillis: Long) : ProbeResult

    data class Unavailable(val reason: String) : ProbeResult
}

/**
 * `LOCATE_NOW` (FR-9): one fix, reported back, hardware released.
 *
 * The fallback to the platform's cached position is the part worth defending. A parent sends this
 * command because they do not know where a phone is, and answering "no position available" when the
 * platform is holding one from twenty minutes ago is withholding the only thing they asked for. So
 * a cached fix is reported — with its true timestamp and its age, never re-dated to now, so what
 * the console shows is "here, twenty minutes ago" and not a false present tense.
 *
 * Nothing here ever invents a position. The one thing worse than no answer is a wrong one, because a
 * parent acts on a map.
 */
class LocationProbe(
    private val source: LocationSource,
    private val now: () -> Long,
    private val timeoutMillis: Long = DEFAULT_TIMEOUT_MILLIS,
) {

    fun probe(): ProbeResult {
        val permitted = try {
            source.permitted()
        } catch (e: RuntimeException) {
            return ProbeResult.Unavailable(
                "whether this device may use location could not be read (${reason(e)})"
            )
        }
        if (!permitted) {
            return ProbeResult.Unavailable(
                "this device has no location permission, so it cannot be located until one is granted"
            )
        }

        val fresh = try {
            source.freshFix(timeoutMillis)
        } catch (e: RuntimeException) {
            null
        }
        if (fresh != null) return ProbeResult.Located(fresh, fresh = true, ageMillis = 0)

        val cached = try {
            source.lastKnownFix()
        } catch (e: RuntimeException) {
            return ProbeResult.Unavailable(
                "no fix within ${timeoutMillis / 1000}s and the last known position could not be " +
                    "read (${reason(e)})"
            )
        }
            ?: return ProbeResult.Unavailable(
                "no fix within ${timeoutMillis / 1000}s and this device has no last known position"
            )

        // Clamped at zero. A fix dated in the future is a clock that moved — the phone's or the
        // GNSS receiver's — and reporting a negative age would put "-3 minutes ago" in front of a
        // parent, which reads as a bug in the console rather than as what it is.
        val age = (now() - cached.capturedAtEpochMillis).coerceAtLeast(0)
        return ProbeResult.Located(cached, fresh = false, ageMillis = age)
    }

    private fun reason(e: RuntimeException): String = e.message ?: e.javaClass.simpleName

    companion object {
        /**
         * Long enough for a cold GNSS fix outdoors, short enough that the parent gets *an* answer —
         * the cached one — while they are still looking at the console.
         */
        const val DEFAULT_TIMEOUT_MILLIS = 30_000L
    }
}
