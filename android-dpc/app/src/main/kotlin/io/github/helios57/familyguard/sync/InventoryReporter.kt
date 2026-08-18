package io.github.helios57.familyguard.sync

import io.github.helios57.familyguard.device.InstalledApp
import io.github.helios57.familyguard.device.InstalledAppReader
import java.io.IOException
import java.security.MessageDigest

/** What one attempt to report the inventory did. */
sealed interface InventoryResult {
    /** The server took it. [stored] is what the *server* says it holds, not what was sent. */
    data class Sent(val sent: Int, val stored: Int) : InventoryResult

    /** Identical to the last inventory the server accepted, so nothing was sent. */
    data object Unchanged : InventoryResult

    /** The device could not read its own app list. Never sent as an empty inventory. */
    data class NotMeasured(val reason: String) : InventoryResult

    /** The read worked and the send did not. The digest is not advanced, so the next sync retries. */
    data class Failed(val cause: Exception) : InventoryResult
}

/**
 * Reports the installed-app inventory when it has changed (FR-5.1).
 *
 * **Sends only on change**, decided by a digest of the list rather than by a timestamp. A phone
 * syncs on every server event and on every package install, and a device with two hundred apps that
 * re-sent all of them each time would spend most of its uplink telling the server what it already
 * knows.
 *
 * **The digest advances only after the server accepts.** That ordering is the whole of the
 * correctness here: recording it first and sending after would make a failed send look identical to
 * an unchanged list, and the newly installed app the parent is waiting to approve would never be
 * reported again — the console would show the phone as healthy and the app as absent.
 */
class InventoryReporter(
    private val reader: InstalledAppReader,
    /** @return the number of apps the server says it stored. */
    private val send: (List<InstalledApp>) -> Int,
    private val lastDigest: () -> String,
    private val recordDigest: (String) -> Unit,
) {

    fun report(): InventoryResult {
        val apps = reader.installed() ?: return InventoryResult.NotMeasured(reader.unavailableReason())
        val digest = digestOf(apps)
        if (digest == lastDigest()) return InventoryResult.Unchanged
        val stored = try {
            send(apps)
        } catch (e: IOException) {
            return InventoryResult.Failed(e)
        }
        recordDigest(digest)
        return InventoryResult.Sent(apps.size, stored)
    }

    private companion object {
        /**
         * A digest over every field that is sent, not just the package names.
         *
         * An app renamed by an update — or one that becomes a system app because the OEM shipped it
         * in an OTA — is a change the console should show. Hashing only the package names would make
         * those changes permanently invisible, and invisible in the direction where the console's
         * copy silently stops matching the phone.
         *
         * Each field is **length-prefixed** rather than joined with a delimiter. An app label is
         * arbitrary user-visible text: it can contain a space, a newline, or whatever character
         * looked safe to separate on, and a delimited encoding lets two different inventories hash
         * identically by moving one character across a boundary. A collision here does not look like
         * a bug — it looks like an inventory that stopped changing.
         */
        fun digestOf(apps: List<InstalledApp>): String {
            val md = MessageDigest.getInstance("SHA-256")
            for (app in apps.sortedBy { it.packageName }) {
                for (field in listOf(app.packageName, app.label, app.systemApp.toString())) {
                    val bytes = field.toByteArray(Charsets.UTF_8)
                    md.update("${bytes.size}:".toByteArray(Charsets.UTF_8))
                    md.update(bytes)
                }
            }
            return md.digest().joinToString("") { "%02x".format(it) }
        }
    }
}
