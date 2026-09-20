package io.github.helios57.familyguard.filter

import android.content.Context
import android.util.Log
import io.github.helios57.familyguard.enroll.EncryptedCredentialStore
import io.github.helios57.familyguard.store.encryptedPreferences
import java.io.File

/**
 * The compiled list and the parent's switches, shared by the two things that need them.
 *
 * One process-wide holder rather than one per caller, because the index is the largest object this
 * app builds — a hundred and eighty thousand rules — and the tunnel and the sync thread both need
 * exactly the same one. Two copies would double the memory on the phone with the least of it, and
 * the tunnel would be filtering from whichever copy happened to be updated last.
 *
 * Everything here is cheap to call. Compilation happens once, on first use, from bytes already on
 * disk — so the tunnel can come up on a phone that has not reached the internet since it booted,
 * which is exactly the minute a filter that waited for a download would be off.
 */
object FilterState {

    private const val TAG = "FGFilter"
    private const val FILE = "ad-filter"
    private const val KEY_ENABLED = "enabled"
    private const val KEY_MODE = "mode"
    private const val KEY_LIST_URL = "list_url"

    private val lock = Any()

    @Volatile private var engine: FilterEngine? = null

    /** The policy and the engine, as one value, so a caller cannot read a mismatched pair. */
    class Snapshot(val policy: FilterPolicy, val engine: FilterEngine, val list: FilterListState)

    fun of(context: Context): Snapshot {
        val policy = policy(context)
        return Snapshot(policy, engine(context, policy), store(context).state())
    }

    fun policy(context: Context): FilterPolicy {
        val preferences = encryptedPreferences(context, FILE)
        return FilterPolicy(
            enabled = preferences.getBoolean(KEY_ENABLED, false),
            mode = runCatching { RouteMode.valueOf(preferences.getString(KEY_MODE, null) ?: "") }
                .getOrDefault(RouteMode.FULL),
        )
    }

    fun listUrl(context: Context): String =
        encryptedPreferences(context, FILE).getString(KEY_LIST_URL, "").orEmpty()

    /**
     * Record what the parent asked for.
     *
     * Persisted rather than held in memory because the service can be restarted by the platform at
     * any time and must not come back up carrying the wrong policy — or, worse, come back up when a
     * parent has just switched it off.
     */
    fun remember(context: Context, policy: FilterPolicy, listUrl: String) {
        encryptedPreferences(context, FILE).edit()
            .putBoolean(KEY_ENABLED, policy.enabled)
            .putString(KEY_MODE, policy.mode.name)
            .putString(KEY_LIST_URL, listUrl)
            .apply()
        // The live engine, if one exists, is what the tunnel is reading right now. Leaving it on
        // the old switch would keep filtering for a child whose parent has just switched it off,
        // until the service happened to restart.
        engine?.update(indexOf(context), policy.enabled)
    }

    /**
     * Fetch the configured list if it has changed, and hand the result to the running tunnel.
     *
     * Called from the sync path, never from the tunnel: this does network IO and the tunnel's
     * reader thread is the one thing on the phone that must never block.
     */
    fun refresh(context: Context): RefreshResult {
        val url = listUrl(context)
        val result = store(context).refresh(url)
        if (result is RefreshResult.Updated) {
            val policy = policy(context)
            // Swapped into the live engine rather than restarting the tunnel: [FilterEngine.update]
            // is safe while packets are flowing, and a restart would drop every open connection to
            // apply a list the child will not notice either way.
            engine(context, policy).update(indexOf(context), policy.enabled)
            Log.i(TAG, "filter list updated: ${result.state.rules} rules, ${result.report}")
        }
        return result
    }

    fun listState(context: Context): FilterListState = store(context).state()

    fun forget(context: Context) {
        store(context).clear()
        engine?.update(DomainIndex.EMPTY, enabled = false)
    }

    private fun engine(context: Context, policy: FilterPolicy): FilterEngine {
        engine?.let { return it }
        synchronized(lock) {
            engine?.let { return it }
            val serverUrl = runCatching { EncryptedCredentialStore(context).load()?.serverUrl }
                .getOrNull()
                .orEmpty()
            val built = FilterEngine(
                initialIndex = indexOf(context),
                // Derived from the one string that has to be right rather than configured beside
                // it: if a downloaded list ever named the control plane, the phone would stop
                // syncing and the only thing that can switch the filter off is a sync.
                neverBlocked = FilterEngine.neverBlockedFor(serverUrl),
                initiallyEnabled = policy.enabled,
            )
            engine = built
            return built
        }
    }

    private fun indexOf(context: Context): DomainIndex =
        store(context).compiled()?.first ?: DomainIndex.EMPTY

    private fun store(context: Context): FilterListStore = FilterListStore(
        directory = File(context.applicationContext.filesDir, "filter"),
        log = { Log.i(TAG, it) },
    )
}
