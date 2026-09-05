package io.github.helios57.familyguard.update

import java.io.File
import java.io.IOException
import java.io.InputStream
import java.io.OutputStream
import java.security.MessageDigest
import java.util.Base64

/** What the control plane says the DPC on this phone should be (`GET /device/apk-info`). */
data class ApkInfo(
    val url: String,
    val packageChecksum: String,
    val size: Long,
)

/**
 * What one APK says about itself — the archive on disk, or the app already installed.
 *
 * [signerSha256] is the SHA-256 of the signing certificate, the same number the provisioning QR
 * carries and the same one `apksigner verify --print-certs` prints. It is hex here rather than the
 * QR's base64 because nothing compares the two across that boundary: this value is only ever
 * compared with another value produced by the same reader.
 */
data class ApkIdentity(
    val versionCode: Long,
    val versionName: String,
    val signerSha256: String,
)

/** What an update attempt did. There is no "probably". */
sealed interface UpdateOutcome {
    /**
     * Verified and staged. [commit] is deliberately not called by [AppUpdater]: committing kills
     * this process, so it must not run until the command has been acknowledged — see
     * [io.github.helios57.familyguard.commands.CommandOutcome.Done].
     */
    data class Staged(
        val identity: ApkIdentity,
        val fromVersionCode: Long,
        val commit: () -> Unit,
    ) : UpdateOutcome

    /** The phone is already running what the server hosts. Not a failure, and not an install. */
    data class AlreadyCurrent(val identity: ApkIdentity) : UpdateOutcome

    /** Nothing was installed, and this is why, in words a parent reads under the command. */
    data class Refused(val reason: String) : UpdateOutcome
}

/**
 * Downloads the DPC this server hosts and installs it over this app (FR-15).
 *
 * **Why the server gets to do this at all.** The APK is not in the control plane's container image;
 * it is a file on the node, installed out of band, and a phone provisioned six months ago has
 * whatever build was current that day. Without this, every fix to the enforcement half of the
 * product needs a factory reset and a re-scan of a QR code, on a phone that belongs to a child.
 *
 * **Five checks, in this order, before anything is committed.** Each one is here because the
 * platform's own version of the same check fails later, more quietly, or on the phone:
 *
 *  1. **The size the server declared.** A download that ends early is the ordinary failure on a
 *     phone, and it is the one that produces a *valid-looking* file. Checked first because it costs
 *     nothing and because it tells the parent "the download was cut off" instead of "the file was
 *     wrong", which sends them to a different place.
 *  2. **The checksum the server published.** The same value in every provisioning QR, computed at
 *     the server from the bytes it serves. This is what makes the download the artifact and not
 *     merely something that arrived over the right URL.
 *  3. **It parses as an APK.** `getPackageArchiveInfo` returning null on a file that hashed
 *     correctly means the server is hosting something that is not an APK, which is a deployment
 *     mistake and not a device problem.
 *  4. **The signing certificate matches the app already installed.** The platform enforces this
 *     too, and refuses the session with `INSTALL_FAILED_UPDATE_INCOMPATIBLE` — after the download,
 *     from inside the installer, with a message nobody sees. Checking here names it. It is also the
 *     check that makes a hostile `apk-info` pointing somewhere else useless: a package signed by
 *     another key cannot replace this one, and it will not be downloaded into a session either.
 *  5. **The version code strictly increases.** Android refuses a downgrade, so installing one is
 *     not a risk — reporting it as an install that will never happen is. An equal version code is
 *     the ordinary case (a parent pressing the button twice) and is answered as "already current"
 *     rather than as an error.
 *
 * Every dependency is a function rather than an Android type, so the whole decision above runs in
 * the JVM suite. The platform half — the package parser and the installer session — is
 * [AndroidInstaller], and it is what the emulator layer exercises.
 */
class AppUpdater(
    private val info: () -> ApkInfo,
    private val open: (url: String) -> InputStream,
    private val staging: () -> File,
    private val identify: (File) -> ApkIdentity?,
    private val installed: () -> ApkIdentity,
    private val install: (File) -> Unit,
    private val log: (String) -> Unit = {},
) {

    fun update(): UpdateOutcome {
        val want = try {
            info()
        } catch (e: Exception) {
            return UpdateOutcome.Refused("the server did not say which build to install (${reason(e)})")
        }
        if (want.packageChecksum.isBlank()) {
            return UpdateOutcome.Refused("the server hosts no DPC to install")
        }

        val file = staging()
        try {
            val downloaded = try {
                download(want.url, file, want.size)
            } catch (e: Exception) {
                return UpdateOutcome.Refused("the download failed (${reason(e)})")
            }

            if (want.size > 0 && downloaded.bytes != want.size) {
                return UpdateOutcome.Refused(
                    "the download ended at ${downloaded.bytes} bytes of the ${want.size} the server declared"
                )
            }
            if (downloaded.checksum != want.packageChecksum) {
                return UpdateOutcome.Refused(
                    "the downloaded file is not the one the server published a checksum for"
                )
            }

            val archive = identify(file)
                ?: return UpdateOutcome.Refused("the server is hosting a file that is not a readable APK")
            val current = installed()

            if (!archive.signerSha256.equals(current.signerSha256, ignoreCase = true)) {
                return UpdateOutcome.Refused(
                    "the download is signed by a different certificate than the app on this phone, " +
                        "so it can never replace it"
                )
            }
            if (archive.versionCode == current.versionCode) {
                return UpdateOutcome.AlreadyCurrent(current)
            }
            if (archive.versionCode < current.versionCode) {
                return UpdateOutcome.Refused(
                    "the server hosts build ${archive.versionCode} and this phone runs " +
                        "${current.versionCode}; Android does not install a downgrade"
                )
            }

            log("staged ${archive.versionName} (build ${archive.versionCode}) over ${current.versionName}")
            // The staged file outlives this function on purpose: the installer reads it during the
            // commit, which happens after the acknowledgement. It is deleted by the next update.
            return UpdateOutcome.Staged(archive, current.versionCode) { install(file) }
        } catch (e: Exception) {
            return UpdateOutcome.Refused(reason(e))
        }
    }

    private data class Downloaded(val bytes: Long, val checksum: String)

    /**
     * Streams the download straight into the staging file, hashing as it goes.
     *
     * Hashing during the copy rather than by re-reading the file afterwards: 13 MB read twice on a
     * phone is not expensive, but the second read is a read of a *different* moment, and this is
     * the function whose whole job is to say what the bytes were.
     */
    private fun download(url: String, into: File, declared: Long): Downloaded {
        val digest = MessageDigest.getInstance("SHA-256")
        var total = 0L
        val ceiling = ceilingFor(declared)
        open(url).use { source ->
            into.outputStream().use { sink ->
                copy(source, sink, ceiling) { chunk, length ->
                    digest.update(chunk, 0, length)
                    total += length
                }
            }
        }
        return Downloaded(total, Base64.getUrlEncoder().withoutPadding().encodeToString(digest.digest()))
    }

    /**
     * How many bytes this download is allowed to write before it is abandoned.
     *
     * The size check in [update] compares totals AFTER the copy, so on its own it is a report and
     * not a limit: a response that never ends fills the phone's storage and is then declared the
     * wrong size, by which point the damage is done. The device the storage belongs to is a child's
     * phone, and a full one stops recording usage and stops taking policy.
     *
     * The server declares the size, so use it — with slack, because a size that is stale by one
     * release must fail as "not the build we published" rather than as a truncated download. When
     * the server declares nothing, [ABSOLUTE_CEILING_BYTES] applies: a DPC is ~13 MB and an APK
     * only grows, so 256 MB is far above anything real and far below a disk.
     */
    private fun ceilingFor(declared: Long): Long =
        if (declared > 0) minOf(declared + SIZE_SLACK_BYTES, ABSOLUTE_CEILING_BYTES) else ABSOLUTE_CEILING_BYTES

    private inline fun copy(source: InputStream, sink: OutputStream, ceiling: Long, seen: (ByteArray, Int) -> Unit) {
        val buffer = ByteArray(64 * 1024)
        var written = 0L
        while (true) {
            val read = source.read(buffer)
            if (read < 0) break
            written += read
            if (written > ceiling) {
                throw IOException("the download passed $ceiling bytes and was abandoned")
            }
            sink.write(buffer, 0, read)
            seen(buffer, read)
        }
        sink.flush()
    }

    /** An exception's message, or its class when it has none — never an empty parenthesis. */
    private fun reason(e: Exception): String = e.message?.takeIf { it.isNotBlank() } ?: e.javaClass.simpleName

    private companion object {
        /**
         * How far past the declared size a download may run before it is abandoned. Big enough that
         * a stale `size` never reads as a truncation, small enough that it is not a storage budget.
         */
        const val SIZE_SLACK_BYTES = 4L * 1024 * 1024

        /** The cap when the server declares no size at all. A DPC is ~13 MB. */
        const val ABSOLUTE_CEILING_BYTES = 256L * 1024 * 1024
    }
}
