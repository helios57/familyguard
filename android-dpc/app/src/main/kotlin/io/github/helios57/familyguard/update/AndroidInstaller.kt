package io.github.helios57.familyguard.update

import android.app.PendingIntent
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.pm.PackageInstaller
import android.content.pm.PackageManager
import android.os.Build
import android.util.Log
import io.github.helios57.familyguard.sync.ConnectionService
import java.io.File
import java.security.MessageDigest

/**
 * The platform half of [AppUpdater]: reading what an APK is, and installing one.
 *
 * Separate from the decision logic because none of this can run off a device — and because the
 * decision is where the mistakes that matter live, so it belongs where the JVM suite can reach it.
 * What is left here is three platform calls, exercised by the emulator layer.
 */
class AndroidInstaller(private val context: Context) {

    /** Where the download is staged. App-private, so no other app can substitute the bytes. */
    fun staging(): File = File(context.cacheDir, STAGED_APK).also { it.delete() }

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
        if (info.packageName != context.packageName) {
            // A different package would install alongside this one rather than replacing it: two
            // device-policy apps on one phone, one of them unmanaged. Refused as unreadable rather
            // than reported as a version, because there is no version of *this* app in it.
            Log.w(TAG, "the hosted APK is ${info.packageName}, not ${context.packageName}")
            return null
        }
        val signer = signerOf(info.signingInfo?.apkContentsSigners) ?: return null
        return ApkIdentity(
            versionCode = info.longVersionCode,
            versionName = info.versionName.orEmpty(),
            signerSha256 = signer,
        )
    }

    /** What this app is, right now, as the package manager sees it. */
    fun installed(): ApkIdentity {
        val flags = PackageManager.GET_SIGNING_CERTIFICATES
        val info = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
            context.packageManager.getPackageInfo(
                context.packageName, PackageManager.PackageInfoFlags.of(flags.toLong())
            )
        } else {
            @Suppress("DEPRECATION")
            context.packageManager.getPackageInfo(context.packageName, flags)
        }
        return ApkIdentity(
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
    fun install(file: File) {
        val installer = context.packageManager.packageInstaller
        val params = PackageInstaller.SessionParams(PackageInstaller.SessionParams.MODE_FULL_INSTALL)
        params.setAppPackageName(context.packageName)
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
            session.commit(statusSender(sessionId))
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
        private const val TAG = "FamilyGuardUpdate"
        private const val STAGED_APK = "familyguard-update.apk"
        private const val ENTRY = "familyguard"
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
