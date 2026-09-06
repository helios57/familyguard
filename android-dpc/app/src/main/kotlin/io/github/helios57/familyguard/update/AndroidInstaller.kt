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
     * Writes the staged APK into an installer session and stops one call short of installing it.
     *
     * **Everything that can fail synchronously happens here, and nothing else does.** `createSession`
     * is what a user restriction refuses, `openWrite` is what a full disk refuses, and both throw —
     * so doing them before the command is acknowledged is what turns "the update did nothing" into
     * a refusal a parent reads under the command they pressed. What is left, [StagedSession.commit],
     * cannot be reported on at all: it kills this process at a moment the platform chooses, which is
     * why [AppUpdater] hands it back rather than calling it.
     *
     * A device owner installs without the "install unknown apps" prompt, so nothing is on screen and
     * nobody taps anything: the child sees the app they cannot remove blink out and come back.
     */
    fun stage(file: File, packageName: String): StagedSession {
        val installer = context.packageManager.packageInstaller
        val sessionId = installer.createSession(sessionParams(packageName))
        try {
            write(installer, sessionId, file)
        } catch (e: Throwable) {
            // An abandoned session frees its staged copy of the APK. Without this a refused update
            // leaves 13 MB per attempt in the installer's spool on a child's phone, and the phone
            // that cannot install is exactly the one that will retry.
            runCatching { installer.abandonSession(sessionId) }
            throw e
        }
        return StagedSession(installer, sessionId, statusSender(packageName.hashCode()))
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
     * [stage] does NOT wait, and the difference is deliberate: what it stages replaces *this* app,
     * so the commit kills the process and there is no "afterwards" to report into.
     */
    fun installAwaiting(file: File, packageName: String): String? =
        awaitStatus(ACTION_MANAGED_INSTALL, packageName) { sender ->
            val installer = context.packageManager.packageInstaller
            val sessionId = installer.createSession(sessionParams(packageName))
            try {
                write(installer, sessionId, file)
                installer.openSession(sessionId).use { it.commit(sender) }
            } catch (e: Throwable) {
                runCatching { installer.abandonSession(sessionId) }
                throw e
            }
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
     * The parameters every session this app opens is created with. One place, so the self-update
     * path and the managed-app path cannot drift into two sets of them.
     *
     * **[PackageInstaller.SessionParams.setRequireUserAction] is the line this whole class exists
     * around, and leaving it out is what made FR-15 fail silently on a real phone.** Its documented
     * default is `USER_ACTION_UNSPECIFIED`, and unspecified means *`USER_ACTION_REQUIRED` for an
     * installer that declares `REQUEST_INSTALL_PACKAGES`* — which this app does, because the
     * declaration is what makes the session permitted at all. So every commit answered
     * `STATUS_PENDING_USER_ACTION`: a request for a tap, handed to an app that is not on screen, on
     * a phone whose owner is a child. Nothing was installed, nothing was shown, this process was
     * not killed, and the only trace was one line in a log nobody can read. Measured 2026-09-06 on
     * the pilot phone: `UPDATE_APP` acknowledged "downloaded and verified; installing now" and the
     * phone was still on the previous build twenty-four hours later.
     *
     * `USER_ACTION_NOT_REQUIRED` is honoured for an installer holding `REQUEST_INSTALL_PACKAGES`
     * when it is updating itself (the self-update) or is the installer of record (every managed app
     * after its first), and when the app declares `UPDATE_PACKAGES_WITHOUT_USER_ACTION` — which the
     * manifest now does, next to the reason. A device owner is separately privileged to install
     * silently, so there are two independent reasons this holds; the receiver reports it either way
     * rather than trusting them.
     */
    private fun sessionParams(packageName: String): PackageInstaller.SessionParams {
        val params = PackageInstaller.SessionParams(PackageInstaller.SessionParams.MODE_FULL_INSTALL)
        params.setAppPackageName(packageName)
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.S) {
            // Says why this install happened, to the platform and to anything auditing it. A device
            // policy install is not a user-initiated one, and the distinction is what keeps this out
            // of the "recently installed by the user" surfaces a child would look at.
            params.setInstallReason(PackageManager.INSTALL_REASON_POLICY)
            params.setRequireUserAction(PackageInstaller.SessionParams.USER_ACTION_NOT_REQUIRED)
        }
        return params
    }

    /** Copies the archive into an open session and makes sure the bytes are actually on disk. */
    private fun write(installer: PackageInstaller, sessionId: Int, file: File) {
        installer.openSession(sessionId).use { session ->
            session.openWrite(ENTRY, 0, file.length()).use { sink ->
                file.inputStream().use { it.copyTo(sink) }
                // Without this the bytes may still be in a buffer when commit() reads the session,
                // and the install fails on a truncated APK that was written correctly.
                session.fsync(sink)
            }
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
 * An installer session with the APK already in it, waiting for the one call that installs it.
 *
 * It exists so that the two halves of an install can happen at two different moments. Creating the
 * session and writing 13 MB into it can fail, and fails synchronously — a user restriction, a full
 * disk — so it happens while there is still an acknowledgement to put the reason in. Committing
 * cannot fail synchronously and cannot be waited for either: it replaces the app this process is,
 * so the platform kills the process, at a moment it chooses, without returning.
 *
 * The session survives being closed. `openSession` is re-openable until the session is committed or
 * abandoned, which is what makes the split possible at all rather than merely desirable.
 */
class StagedSession internal constructor(
    private val installer: PackageInstaller,
    private val sessionId: Int,
    private val sender: android.content.IntentSender,
) {
    /** **Kills this process.** Everything owed to anyone else must already have happened. */
    fun commit() {
        installer.openSession(sessionId).use { it.commit(sender) }
    }

    /** Frees the staged copy. Called when the install is abandoned before it is committed. */
    fun abandon() {
        runCatching { installer.abandonSession(sessionId) }
    }
}

/**
 * What the installer said about a session this app committed, **recorded where a parent can see it**.
 *
 * Reached in two situations, and they are opposite. A session that *failed* reports here while this
 * process is still alive, and that report is the only record of it — the command was acknowledged
 * as "installing" before the commit, so a silent failure leaves the console showing an update that
 * never happened. A session that *succeeded* usually kills this process before the broadcast is
 * delivered; when it does arrive, it arrives in the new version.
 *
 * **Logging it was not enough, and that is not a theory.** Until 2026-09-06 this receiver wrote one
 * `Log.e` line and stopped there. On the pilot phone every session was answered
 * `STATUS_PENDING_USER_ACTION` — [AndroidInstaller.sessionParams] explains why — and the whole
 * failure consisted of that line, in a log on a phone in a school bag, while the console showed an
 * acknowledged command and a version that never moved. So the reason now goes into [UpdateReport],
 * which puts it on the next heartbeat.
 */
class UpdateStatusReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        if (intent.action != AndroidInstaller.ACTION_INSTALL_STATUS) return
        val status = intent.getIntExtra(PackageInstaller.EXTRA_STATUS, Int.MIN_VALUE)
        val message = intent.getStringExtra(PackageInstaller.EXTRA_STATUS_MESSAGE).orEmpty()
        val report = androidUpdateReport(context)
        when (status) {
            PackageInstaller.STATUS_SUCCESS -> {
                Log.i(TAG, "self-update installed")
                report.clear()
            }
            PackageInstaller.STATUS_PENDING_USER_ACTION -> {
                // Not expected on a fully managed device: a device owner installs without a prompt,
                // and the session is created asking for none. If it happens anyway the update is
                // stuck, and saying so to the parent is worth more than launching a dialog onto a
                // child's screen for an app they did not ask to update.
                val reason = "Android asked for someone to confirm this install; a device owner " +
                    "should never be asked, so nothing was installed"
                Log.e(TAG, reason)
                report.record(reason, runningVersionCode(context))
            }
            else -> {
                val reason = "Android refused the update: status=$status " +
                    message.ifEmpty { "(no message)" }
                Log.e(TAG, reason)
                report.record(reason, runningVersionCode(context))
            }
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
