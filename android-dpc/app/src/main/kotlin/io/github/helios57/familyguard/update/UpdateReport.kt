package io.github.helios57.familyguard.update

/**
 * One self-update attempt that did not end with a new build running, as the platform described it.
 *
 * [fromVersionCode] is the build that was running when the attempt failed, and it is what makes the
 * record self-clearing: any build above it is proof that a later attempt succeeded, so the failure
 * can be dropped without anything having to remember to drop it. Storing the *target* instead would
 * be a number this device never observed — the target comes from the server, and a server that has
 * since published a different one would leave a record that could never be satisfied.
 */
data class UpdateFailure(
    val reason: String,
    val fromVersionCode: Long,
    val atEpochMillis: Long,
)

/** Where [UpdateReport] keeps its one record. Survives a reboot and survives the update itself. */
interface UpdateReportStore {
    fun load(): UpdateFailure?
    fun save(failure: UpdateFailure?)
}

/** An [UpdateReportStore] that forgets everything when the process ends. Tests, and nothing else. */
class InMemoryUpdateReportStore(private var value: UpdateFailure? = null) : UpdateReportStore {
    override fun load(): UpdateFailure? = value
    override fun save(failure: UpdateFailure?) {
        value = failure
    }
}

/**
 * Why the last self-update did not happen, kept until a newer build is running (FR-15.7).
 *
 * **This exists because FR-15 had no failure channel at all, and the failure it had was silent.**
 * The `UPDATE_APP` acknowledgement is sent *before* the install — it has to be, because the install
 * kills the process that would send it — so "ACKED: downloaded and verified; installing now" is a
 * statement about the future and says nothing about whether the install happened. When it did not,
 * everything a parent could see stayed green: the command was acknowledged, the phone kept
 * heartbeating, and the version it reported simply never changed. Measured on the pilot phone on
 * 2026-09-06, where the cause was a session the platform answered with `STATUS_PENDING_USER_ACTION`
 * and a receiver that logged it to a log nobody on this side of the network can read.
 *
 * So the platform's own words are kept here and sent on the next heartbeat, where the console shows
 * them under the device. Two writers reach it: [UpdateStatusReceiver], for what the installer said
 * about a committed session, and the automatic update loop, for a refusal that never got that far.
 *
 * It clears itself rather than being cleared by whoever fixes the problem — see [UpdateFailure].
 */
class UpdateReport(
    private val store: UpdateReportStore,
    private val now: () -> Long = System::currentTimeMillis,
) {

    /**
     * Records why an attempt made from [runningVersionCode] did not install anything.
     *
     * The latest attempt wins. A device that has failed twice for two reasons has one problem to
     * report and it is the current one; a list would be a log, and this is a status.
     */
    fun record(reason: String, runningVersionCode: Long) {
        store.save(UpdateFailure(reason.trim(), runningVersionCode, now()))
    }

    /**
     * What to report on the heartbeat: the reason, or "" when there is nothing to report.
     *
     * @param runningVersionCode this build, read from the package manager. **Zero means the package
     * manager could not answer**, and zero is deliberately not treated as "newer than nothing": a
     * failure recorded against build 8 stays until a build above 8 is actually seen running.
     */
    fun pending(runningVersionCode: Long): String {
        val failure = store.load() ?: return ""
        if (runningVersionCode > failure.fromVersionCode) {
            store.save(null)
            return ""
        }
        return failure.reason
    }

    /** Drops the record. Called when the installer reports a session that actually succeeded. */
    fun clear() {
        if (store.load() != null) store.save(null)
    }
}
