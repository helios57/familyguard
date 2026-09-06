package io.github.helios57.familyguard.update

import android.content.Context
import android.content.pm.PackageManager

/**
 * The on-device [UpdateReport], and the running build it measures itself against.
 *
 * **Plain preferences, not encrypted ones.** Nothing here is a secret — the reason is written to
 * describe a failure to a parent, and it travels to the server on the next heartbeat in clear. The
 * encrypted stores exist for the credential and the recovery lockout, and putting a status field in
 * one of them would tie it to a keystore unwrap that can fail on a phone whose whole problem is
 * that something is already failing.
 *
 * Its own file, for the reason `ConnectionService`'s inventory digest has its own: clearing the
 * policy cache or the credential must not take the record of an update that did not happen with it.
 * The record is written by [UpdateStatusReceiver], which runs in whatever process the platform
 * happens to deliver a broadcast to, and read by the service — a file is what they share.
 */
fun androidUpdateReport(context: Context): UpdateReport =
    UpdateReport(AndroidUpdateReportStore(context.applicationContext))

/**
 * This app's own version code, or 0 when the package manager cannot answer about the package it is
 * running. Zero is "not measured" and [UpdateReport] treats it as such, never as build zero.
 */
fun runningVersionCode(context: Context): Long = try {
    context.packageManager.getPackageInfo(context.packageName, 0).longVersionCode
} catch (e: PackageManager.NameNotFoundException) {
    0L
}

/**
 * The on-device [UpdateSchedule].
 *
 * The same file as the report, and deliberately: both are the state of one feature, and a phone
 * that kept the reason an update failed but forgot when to try again would retry on the schedule of
 * a phone that had never tried at all.
 */
fun androidUpdateSchedule(context: Context): UpdateSchedule =
    UpdateSchedule(AndroidUpdateScheduleStore(context.applicationContext))

/** The one preferences file this feature owns. See [androidUpdateReport] for why it is its own. */
private const val FILE = "family-guard-update"

private class AndroidUpdateScheduleStore(context: Context) : UpdateScheduleStore {

    private val preferences = context.getSharedPreferences(FILE, Context.MODE_PRIVATE)

    override fun dueAt(): Long = preferences.getLong(KEY_DUE, 0L)

    /**
     * `commit`, for the same reason [AndroidUpdateReportStore.save] uses it: this is written just
     * before the commit that ends this process, and a due instant that never reached disk is a
     * phone that asks again in two minutes forever.
     */
    override fun setDueAt(atEpochMillis: Long) {
        preferences.edit().putLong(KEY_DUE, atEpochMillis).commit()
    }

    private companion object {
        const val KEY_DUE = "next_check_at"
    }
}

private class AndroidUpdateReportStore(context: Context) : UpdateReportStore {

    private val preferences = context.getSharedPreferences(FILE, Context.MODE_PRIVATE)

    override fun load(): UpdateFailure? {
        val reason = preferences.getString(KEY_REASON, null) ?: return null
        return UpdateFailure(
            reason = reason,
            fromVersionCode = preferences.getLong(KEY_FROM, 0L),
            atEpochMillis = preferences.getLong(KEY_AT, 0L),
        )
    }

    /**
     * `commit`, not `apply`. The writer is a broadcast receiver whose process the platform is free
     * to kill the moment `onReceive` returns, and an asynchronous write that never reached disk is
     * exactly the silent failure this record exists to end.
     */
    override fun save(failure: UpdateFailure?) {
        val editor = preferences.edit()
        if (failure == null) {
            editor.remove(KEY_REASON).remove(KEY_FROM).remove(KEY_AT)
        } else {
            editor.putString(KEY_REASON, failure.reason)
                .putLong(KEY_FROM, failure.fromVersionCode)
                .putLong(KEY_AT, failure.atEpochMillis)
        }
        editor.commit()
    }

    private companion object {
        const val KEY_REASON = "reason"
        const val KEY_FROM = "from_version_code"
        const val KEY_AT = "at"
    }
}
