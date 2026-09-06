package io.github.helios57.familyguard.update

import android.content.pm.PackageInstaller

/**
 * Package names whose block is Google Play Protect. Play Protect is a component of Google Play
 * services rather than an app of its own, so the platform names its host, and a parent handed
 * `com.google.android.gms` learns nothing they can act on — while "Play Protect" is the exact
 * wording that was on the screen in front of them.
 */
private val PLAY_PROTECT = setOf("com.google.android.gms", "com.android.vending")

/**
 * What the platform said about a session that did not install, in words a parent can act on.
 *
 * **`EXTRA_OTHER_PACKAGE_NAME` is the reason this function exists.** `STATUS_FAILURE_BLOCKED` is
 * documented as *"a device policy may be blocking the operation, a package verifier may have
 * blocked the operation, or the app may be required for core system operation"* — three unrelated
 * causes behind one integer — and the platform disambiguates them by naming the blocking package in
 * a second extra that this app read and discarded until 2026-09-06. On a phone whose sideload had
 * just been stopped by Play Protect, the console would have shown `status=2 (no message)`: a number
 * that is not wrong and cannot be acted on.
 *
 * Every branch keeps the platform's own [message] when there is one, because that is what FR-15.7
 * promises to report; the sentence in front of it is this app's, and it says which of the three
 * things happened.
 *
 * @param status the `EXTRA_STATUS` of an install or uninstall broadcast.
 * @param message its `EXTRA_STATUS_MESSAGE`, or "" when it carried none.
 * @param blockedBy its `EXTRA_OTHER_PACKAGE_NAME` — the blocking or conflicting package — or null.
 * @return "" when [status] is a success, and a sentence for everything else. The empty string is
 * the same success sentinel [AndroidInstaller] already returns, so a caller cannot mistake a
 * failure for a pass by forgetting to compare.
 */
fun installFailureReason(status: Int, message: String, blockedBy: String? = null): String {
    if (status == PackageInstaller.STATUS_SUCCESS) return ""
    val other = blockedBy?.trim().orEmpty()
    val head = when (status) {
        // Not an outcome: the platform is asking for a tap that a device owner should never be
        // asked for, on a phone whose owner is a child. Named, because "the install failed" would
        // send the reader to look at the download.
        PackageInstaller.STATUS_PENDING_USER_ACTION ->
            "Android asked for someone to confirm this install; a device owner should never be " +
                "asked, so nothing was installed"
        PackageInstaller.STATUS_FAILURE_BLOCKED -> when {
            other in PLAY_PROTECT ->
                "Google Play Protect blocked this install because it does not recognise this app"
            other.isNotEmpty() -> "$other blocked this install"
            else -> "something on this phone blocked this install"
        }
        PackageInstaller.STATUS_FAILURE_ABORTED -> "the install was stopped before it finished"
        PackageInstaller.STATUS_FAILURE_INVALID -> "Android rejected the archive as invalid"
        PackageInstaller.STATUS_FAILURE_CONFLICT -> when {
            other.isNotEmpty() -> "the install conflicts with $other, which is already installed"
            else -> "the install conflicts with an app already on this phone"
        }
        PackageInstaller.STATUS_FAILURE_STORAGE -> "this phone does not have room for the download"
        PackageInstaller.STATUS_FAILURE_INCOMPATIBLE -> "this build does not run on this phone"
        PackageInstaller.STATUS_FAILURE_TIMEOUT -> "the install did not finish in time"
        PackageInstaller.STATUS_FAILURE -> "Android refused the install without saying which part failed"
        else -> "Android reported an install status this app does not recognise"
    }
    val said = message.trim()
    return buildString {
        append(head)
        if (said.isNotEmpty()) append(" — Android said: ").append(said)
        append(" (status ").append(status).append(')')
    }
}
