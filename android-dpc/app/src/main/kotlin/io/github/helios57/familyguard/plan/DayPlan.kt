package io.github.helios57.familyguard.plan

import android.content.Context
import android.content.SharedPreferences
import io.github.helios57.familyguard.store.encryptedPreferences
import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.Json

/**
 * The day as the plan sees it (FR-22), exactly as the server sends it beside the policy: the groups
 * that run today, each task's state, and the earned-time balance with the credits that make it up.
 */
@Serializable
data class DayPlan(
    @SerialName("day") val day: String = "",
    @SerialName("groups") val groups: List<DayGroup> = emptyList(),
    @SerialName("earned") val earned: DayEarned = DayEarned(),
)

@Serializable
data class DayGroup(
    @SerialName("id") val id: String = "",
    @SerialName("title") val title: String = "",
    @SerialName("starts_at") val startsAt: String = "",
    @SerialName("ends_at") val endsAt: String = "",
    @SerialName("earned_minutes") val earnedMinutes: Int = 0,
    /** Whether now is inside the group's window: the only time its tasks can be reported. */
    @SerialName("open") val open: Boolean = false,
    @SerialName("credited_minutes") val creditedMinutes: Int = 0,
    @SerialName("tasks") val tasks: List<DayTask> = emptyList(),
)

@Serializable
data class DayTask(
    @SerialName("id") val id: String = "",
    @SerialName("title") val title: String = "",
    @SerialName("note") val note: String = "",
    @SerialName("state") val state: String = OPEN,
) {
    companion object {
        const val OPEN = "OPEN"
        const val REPORTED = "REPORTED"
        const val CONFIRMED = "CONFIRMED"
        const val REJECTED = "REJECTED"
    }
}

@Serializable
data class DayEarned(
    @SerialName("available_minutes") val availableMinutes: Int = 0,
    @SerialName("spent_minutes") val spentMinutes: Int = 0,
    @SerialName("left_minutes") val leftMinutes: Int = 0,
    @SerialName("credits") val credits: List<DayCredit> = emptyList(),
)

@Serializable
data class DayCredit(
    @SerialName("earned_on") val earnedOn: String = "",
    @SerialName("expires_on") val expiresOn: String = "",
    @SerialName("minutes") val minutes: Int = 0,
)

/** What the Heute screen offers, decided without a view. */
object DayPlanView {

    /** "Fertig" is offered inside the group's window, for a task that is open or was not accepted. */
    fun canReport(group: DayGroup, task: DayTask): Boolean =
        group.open && (task.state == DayTask.OPEN || task.state == DayTask.REJECTED)

    /** The credit with minutes left that runs out first, or null. */
    fun soonestExpiry(credits: List<DayCredit>): DayCredit? =
        credits.filter { it.minutes > 0 }.minByOrNull { it.expiresOn }
}

/** Where the last day plan the server sent is kept, so the Heute screen shows it offline. */
interface DayPlanStore {
    fun load(): DayPlan?
    fun save(plan: DayPlan)
}

/** [DayPlanStore] in its own encrypted preferences file. */
class EncryptedDayPlanStore(context: Context) : DayPlanStore {
    private val json = Json { ignoreUnknownKeys = true }
    private val preferences: SharedPreferences by lazy { encryptedPreferences(context, FILE) }

    override fun load(): DayPlan? {
        val stored = preferences.getString(KEY, null) ?: return null
        return runCatching { json.decodeFromString(DayPlan.serializer(), stored) }.getOrNull()
    }

    override fun save(plan: DayPlan) {
        preferences.edit().putString(KEY, json.encodeToString(DayPlan.serializer(), plan)).commit()
    }

    private companion object {
        const val FILE = "family-guard-dayplan"
        const val KEY = "plan"
    }
}
