package io.github.helios57.familyguard.update

import android.app.PendingIntent
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.content.pm.PackageInstaller
import android.content.pm.PackageManager
import android.os.Build
import android.util.Log
import io.github.helios57.familyguard.sync.ConnectionService
import java.io.File
import java.security.MessageDigest
import java.util.concurrent.ArrayBlockingQueue
import java.util.concurrent.TimeUnit

/**
 * The platform half of [AppUpdater]: reading what an APK is, and installing one.
 *
 * Separate from the decision logic because none of this can run off a device — and because the
 * decision is where the mistakes that matter live, so it belongs where the JVM suite can reach it.
 * What is left here is three platform calls, exercised by the emulator layer.
 */
class AndroidInstaller(private val context: Context) {

    /**
     * Where a download is staged. App-private, so no other app can substitute the bytes.
     *
     * One file per package rather than one file: the managed-app applier converges several
     * applications in a single pass, and a shared spool would have the second download overwrite
     * the first while its session was still reading it. The name is derived from the package, which
     * the caller has already had to match against the archive, so it cannot contain a separator.
     */
    fun staging(packageName: String): File =
        File(context.cacheDir, "staged-$packageName.apk").also { it.delete() }

    /**
     * What an APK file on disk says about itself, or null when the platform cannot parse it.
     *
     * `sourceDir` and `publicSourceDir` are filled in by hand. `getPackageArchiveInfo` populates
     * the rest of `ApplicationInfo` from the archive but leaves those two null, and several
     * platform paths that read signing information off an `ApplicationInfo` then throw or return
     * nothing — the symptom is an unsigned-looking APK rather than an error.
     */
    fun identify(file: File): ApkIdentity? {
        val flags = PackageManager.GET_SIGNING_CERTIFICATES
        val info = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
            context.packageManager.getPackageArchiveInfo(
                file.absolutePath, PackageManager.PackageInfoFlags.of(flags.toLong())
            )
        } else {
            @Suppress("DEPRECATION")
            context.packageManager.getPackageArchiveInfo(file.absolutePath, flags)
        } ?: return null
        info.applicationInfo?.let {
            it.sourceDir = file.absolutePath
            it.publicSourceDir = file.absolutePath
        }
        // Which package is acceptable is NOT decided here. This reads what the archive is; whether
        // that is what was asked for is [AppUpdater]'s check 4, and it lives there because the
        // answer differs per caller — the DPC's own package for a self-update, the parent's
        // declared package for a managed app — and because a refusal that reads "not a readable
        // APK" for a file that parsed perfectly is a diagnosis pointing away from the fault.
        val signer = signerOf(info.signingInfo?.apkContentsSigners) ?: return null
        return ApkIdentity(
            packageName = info.packageName,
            versionCode = info.longVersionCode,
            versionName = info.versionName.orEmpty(),
            signerSha256 = signer,
        )
    }

    /**
     * What a package is on this phone right now, or null when it is not installed.
     *
     * Null rather than an exception, and null rather than an empty [ApkIdentity]: "not installed"
     * is the ordinary state of every managed app before its first install, and both alternatives
     * make the caller either catch an expected case or compare against a signer of "".
     *
     * A device owner is exempt from package visibility filtering, so this sees every package on
     * the device and not only the ones this app declares an interest in. `NameNotFoundException`
     * here therefore means absent, not hidden.
     */
    fun installed(packageName: String): ApkIdentity? {
        val flags = PackageManager.GET_SIGNING_CERTIFICATES
        val info = try {
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
                context.packageManager.getPackageInfo(
                    packageName, PackageManager.PackageInfoFlags.of(flags.toLong())
                )
            } else {
                @Suppress("DEPRECATION")
                context.packageManager.getPackageInfo(packageName, flags)
            }
        } catch (e: PackageManager.NameNotFoundException) {
            return null
        }
        return ApkIdentity(
            packageName = info.packageName,
            versionCode = info.longVersionCode,
            versionName = info.versionName.orEmpty(),
            signerSha256 = signerOf(info.signingInfo?.apkContentsSigners).orEmpty(),
        )
    }

    /**
     * Commits the staged APK over this app.
     *
     * **This kills the calling process**, at a moment the platform chooses, without returning. Every
     * caller therefore has to have finished what it owes anyone else first — the command
     * acknowledgement above all, which is why [AppUpdater] hands the commit back rather than calling
     * it. A device owner does not get the "install unknown apps" prompt, so nothing is on screen and
     * nobody taps anything: the child sees the app they cannot remove blink out and come back.
     *
     * `commit` needs a status receiver, and this one is real rather than a placeholder: the statuses
     * that arrive *before* the process dies are the only report of a session that failed to install
     * at all, and without a receiver they would be dropped.
     */
    fun install(file: File, packageName: String) {
        openAndCommit(file, packageName, statusSender(packageName.hashCode()))
    }

    /**
     * Installs a managed app and waits for the platform to say what happened.
     *
     * @return null when the platform reported success, otherwise what it said.
     *
     * Waiting is the difference between a parent seeing "muplay could not be installed:
     * INSTALL_FAILED_INSUFFICIENT_STORAGE" and seeing nothing at all. The managed-app pass
     * converges, so a failed install is simply retried on the next sync — and a retry with no
     * report is a phone that tries forever and a console that shows the app as pending forever,
     * which is the shape of every unfalsifiable green in this repository.
     *
     * [install] does NOT wait, and the difference is deliberate: it replaces *this* app, so the
     * commit kills the process and there is no "afterwards" to report into.
     */
    fun installAwaiting(file: File, packageName: String): String? =
        awaitStatus(ACTION_MANAGED_INSTALL, packageName) { sender ->
            openAndCommit(file, packageName, sender)
        }

    /**
     * Removes a package and waits for the platform to say what happened.
     *
     * @return null when the platform reported success, otherwise what it said.
     */
    fun uninstallAwaiting(packageName: String): String? =
        awaitStatus(ACTION_MANAGED_UNINSTALL, packageName) { sender ->
            context.packageManager.packageInstaller.uninstall(packageName, sender)
        }

    /**
     * Every package on this device that this app is recorded as having installed, excluding itself.
     *
     * This is the authority for "what did the declared set put here", and it is the platform's
     * record rather than this app's own. A local list would have to survive an update, a clear-data
     * and a restore, and each of those produces the same failure: a package this app installed that
     * it no longer recognises, left on a child's phone with nothing able to take it off.
     *
     * Its own package is excluded because a self-update (FR-15) records this app as its own
     * installer, and a managed-app pass that saw itself in this set would find it undeclared —
     * the catalog refuses to hold it, FR-16.6 — and uninstall the device owner.
     */
    fun installedByThisApp(): Set<String> {
        val pm = context.packageManager
        @Suppress("DEPRECATION")
        val all = pm.getInstalledPackages(0)
        return all.asSequence()
            .map { it.packageName }
            .filter { it != context.packageName }
            .filterTo(sortedSetOf()) { installerOf(it) == context.packageName }
    }

    private fun installerOf(packageName: String): String? = try {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.R) {
            context.packageManager.getInstallSourceInfo(packageName).installingPackageName
        } else {
            @Suppress("DEPRECATION")
            context.packageManager.getInstallerPackageName(packageName)
        }
    } catch (e: IllegalArgumentException) {
        // The package went away between the enumeration and this call. Not this app's, then.
        null
    } catch (e: PackageManager.NameNotFoundException) {
        null
    }

    /**
     * One installer session, from create to commit. The single place a session is opened, so the
     * self-update path and the managed-app path cannot drift into two sets of session parameters.
     */
    private fun openAndCommit(file: File, packageName: String, sender: android.content.IntentSender) {
        val installer = context.packageManager.packageInstaller
        val params = PackageInstaller.SessionParams(PackageInstaller.SessionParams.MODE_FULL_INSTALL)
        params.setAppPackageName(packageName)
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.S) {
            // Says why this install happened, to the platform and to anything auditing it. A device
            // policy install is not a user-initiated one, and the distinction is what keeps this out
            // of the "recently installed by the user" surfaces a child would look at.
            params.setInstallReason(PackageManager.INSTALL_REASON_POLICY)
        }
        val sessionId = installer.createSession(params)
        installer.openSession(sessionId).use { session ->
            session.openWrite(ENTRY, 0, file.length()).use { sink ->
                file.inputStream().use { it.copyTo(sink) }
                // Without this the bytes may still be in a buffer when commit() reads the session,
                // and the install fails on a truncated APK that was written correctly.
                session.fsync(sink)
            }
            session.commit(sender)
        }
    }

    /**
     * Runs one installer operation and reports what the platform said, or null on success.
     *
     * The receiver is registered BEFORE the operation opens. A status broadcast delivered with
     * nothing listening is indistinguishable from a session that never reported, and both read as
     * a timeout — which would be recorded against an install that had actually succeeded.
     *
     * A timeout is reported as itself rather than as a failure. The operation may still be
     * proceeding; what is certain is that this pass did not see the end of it, and the next sync
     * will find out by looking at the package list.
     */
    private fun awaitStatus(
        action: String,
        packageName: String,
        body: (android.content.IntentSender) -> Unit,
    ): String? {
        val results = ArrayBlockingQueue<String>(4)
        val receiver = object : BroadcastReceiver() {
            override fun onReceive(ctx: Context, intent: Intent) {
                if (intent.getStringExtra(PackageInstaller.EXTRA_PACKAGE_NAME) != null &&
                    intent.getStringExtra(PackageInstaller.EXTRA_PACKAGE_NAME) != packageName
                ) {
                    return
                }
                val code = intent.getIntExtra(PackageInstaller.EXTRA_STATUS, Int.MIN_VALUE)
                val message = intent.getStringExtra(PackageInstaller.EXTRA_STATUS_MESSAGE).orEmpty()
                results.offer(
                    when (code) {
                        PackageInstaller.STATUS_SUCCESS -> SUCCESS
                        // Not an outcome: the platform is asking for a tap that a device owner
                        // should never be asked for. Named, because the operation is now stuck and
                        // "timed out" would send the reader to look at the network.
                        PackageInstaller.STATUS_PENDING_USER_ACTION ->
                            "the platform asked for a person to confirm this, which a device owner should not be asked"
                        else -> "status=$code ${message.ifEmpty { "(no message)" }}"
                    }
                )
            }
        }
        val filter = IntentFilter(action)
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
            context.registerReceiver(receiver, filter, Context.RECEIVER_NOT_EXPORTED)
        } else {
            @Suppress("UnspecifiedRegisterReceiverFlag")
            context.registerReceiver(receiver, filter)
        }
        try {
            val intent = Intent(action).setPackage(context.packageName)
            val flags = PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_MUTABLE
            val sender = PendingIntent
                .getBroadcast(context, (action + packageName).hashCode(), intent, flags)
                .intentSender
            body(sender)
            val reported = results.poll(STATUS_TIMEOUT_SECONDS, TimeUnit.SECONDS)
                ?: return "the platform reported nothing within ${STATUS_TIMEOUT_SECONDS}s"
            return reported.takeIf { it != SUCCESS }
        } catch (e: RuntimeException) {
            return e.message ?: e.javaClass.simpleName
        } finally {
            runCatching { context.unregisterReceiver(receiver) }
        }
    }

    private fun statusSender(sessionId: Int): android.content.IntentSender {
        val intent = Intent(context, UpdateStatusReceiver::class.java).setAction(ACTION_INSTALL_STATUS)
        // MUTABLE because the installer fills the result extras into this intent. An immutable one
        // is delivered empty, which reads as a session that reported nothing.
        val flags = PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_MUTABLE
        return PendingIntent.getBroadcast(context, sessionId, intent, flags).intentSender
    }

    private fun signerOf(signatures: Array<android.content.pm.Signature>?): String? {
        val first = signatures?.firstOrNull() ?: return null
        val digest = MessageDigest.getInstance("SHA-256").digest(first.toByteArray())
        return digest.joinToString("") { "%02x".format(it) }
    }

    companion object {
        private const val ENTRY = "familyguard"

        /** A sentinel, not a message: [awaitStatus] returns null for success and text for anything else. */
        private const val SUCCESS = ""

        /**
         * How long one install or uninstall is waited for. Long enough for a large APK on a slow
         * phone; short enough that a stuck session does not hold the sync loop for a whole interval.
         */
        private const val STATUS_TIMEOUT_SECONDS = 120L

        const val ACTION_MANAGED_INSTALL = "io.github.helios57.familyguard.MANAGED_INSTALL_STATUS"
        const val ACTION_MANAGED_UNINSTALL = "io.github.helios57.familyguard.MANAGED_UNINSTALL_STATUS"
        const val ACTION_INSTALL_STATUS = "io.github.helios57.familyguard.INSTALL_STATUS"
    }
}

/**
 * What the installer said about a session this app committed.
 *
 * Reached in two situations, and they are opposite. A session that *failed* reports here while this
 * process is still alive, and that report is the only record of it — the command was acknowledged
 * as "installing" before the commit, so a silent failure would leave the console showing an update
 * that never happened. A session that *succeeded* usually kills this process before the broadcast
 * is delivered; when it does arrive, it arrives in the new version.
 */
class UpdateStatusReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        if (intent.action != AndroidInstaller.ACTION_INSTALL_STATUS) return
        val status = intent.getIntExtra(PackageInstaller.EXTRA_STATUS, Int.MIN_VALUE)
        val message = intent.getStringExtra(PackageInstaller.EXTRA_STATUS_MESSAGE).orEmpty()
        when (status) {
            PackageInstaller.STATUS_SUCCESS ->
                Log.i(TAG, "self-update installed")
            PackageInstaller.STATUS_PENDING_USER_ACTION ->
                // Not expected on a fully managed device: a device owner installs without a prompt.
                // If it happens the update is stuck, and saying so is worth more than launching a
                // dialog onto a child's screen for an app they did not ask to update.
                Log.e(TAG, "the platform wants a person to confirm this install; a device owner should not be asked")
            else ->
                Log.e(TAG, "self-update failed: status=$status $message")
        }
    }

    private companion object {
        const val TAG = "FamilyGuardUpdate"
    }
}

/**
 * Restarts the connection after this app has been replaced (FR-15.5).
 *
 * Without it an update is a phone that stops enforcing until the next reboot. `MY_PACKAGE_REPLACED`
 * is delivered to the new version, and it is the only signal it gets: the process the update killed
 * held the foreground service, and nothing restarts a service whose process was replaced. A phone
 * that took an update and went quiet is the worst outcome this feature could have — the console
 * would show it offline and a parent would blame the network.
 */
class PackageReplacedReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        if (intent.action != Intent.ACTION_MY_PACKAGE_REPLACED) return
        Log.i(TAG, "this app was replaced; restarting the connection")
        ConnectionService.start(context, null)
    }

    private companion object {
        const val TAG = "FamilyGuardUpdate"
    }
}
