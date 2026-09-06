package io.github.helios57.familyguard.update

import java.io.File
import java.io.IOException
import java.io.InputStream
import java.io.OutputStream
import java.security.MessageDigest
import java.util.Base64

/**
 * What the control plane says one application on this phone should be.
 *
 * Two sources produce it and they are different endpoints for the same shape: `GET
 * /device/apk-info` for the DPC's own build (FR-15.1), and one entry of the desired state's
 * `managed_apps` for an application a parent declared (FR-16.3).
 *
 * [packageName] is what the caller expects the archive to contain, and it is stated rather than
 * inferred. The self-update path passes this app's own package, so an `apk-info` pointing at some
 * other application is refused before it is parsed for a version; the managed-app path passes the
 * package the parent declared, so a catalog row whose file was swapped for a different application
 * cannot install that application instead.
 *
 * [versionCode] is what makes an update check affordable enough to run on a timer (FR-15.6). It is
 * a *claim*, not a verification: the archive's own version code is read after the download and is
 * what every decision below the download is made on. Zero means the server did not say, which is
 * what an older control plane sends, and the answer to that is to download and find out — the
 * behaviour every caller had before this field existed.
 */
data class ApkInfo(
    val packageName: String,
    val url: String,
    val packageChecksum: String,
    val size: Long,
    val versionCode: Long = 0,
    val versionName: String = "",
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
    val packageName: String,
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
 * Downloads an APK the server hosts and installs it on this phone.
 *
 * Two callers, one decision. The DPC replacing itself (FR-15) and a declared application arriving
 * on a child's phone (FR-16.3) differ only in which package they name and in when the install is
 * committed; every check between the URL and the session is the same, and writing it twice is
 * writing the second copy without the reasons.
 *
 * **Why the server gets to replace this app at all.** The APK is not in the control plane's
 * container image; it is a file on the node, installed out of band, and a phone provisioned six
 * months ago has whatever build was current that day. Without this, every fix to the enforcement
 * half of the product needs a factory reset and a re-scan of a QR code, on a phone that belongs to
 * a child.
 *
 * **One check before the download, and six after it.** The first is the cheap one that makes an
 * automatic update loop affordable: when the server has declared which version it hosts and that
 * version is not newer than what is installed, the attempt ends there, having spent one small
 * request instead of 13 MB. It can only ever end an attempt early — see the comment on the
 * comparison — so nothing below it is weakened by trusting a claim.
 *
 * **The other six, in this order, before anything is committed.** Each one is here because the
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
 *  4. **It is the package that was asked for.** A different package does not replace anything — it
 *     installs *alongside*, so a self-update pointed at the wrong file would leave two device
 *     policy apps on one phone, and a declared app whose file was swapped would put an application
 *     on a child's phone that no parent chose. The platform reports neither as an error: both are
 *     successful installs of something else.
 *  5. **The signing certificate matches the app already installed** — when there is one. The
 *     platform enforces this too, and refuses the session with
 *     `INSTALL_FAILED_UPDATE_INCOMPATIBLE`: after the download, from inside the installer, with a
 *     message nobody sees. Checking here names it. On a **first** install there is nothing on the
 *     phone to compare against and the check is skipped rather than faked — what the first build of
 *     a managed app is trusted against is the catalog's signer pin (FR-16.4), which lives at the
 *     server because that is where the second build can be compared with the first.
 *  6. **The version code strictly increases.** Android refuses a downgrade, so installing one is
 *     not a risk — reporting it as an install that will never happen is. An equal version code is
 *     the ordinary case (a parent pressing the button twice, or a managed app that is already
 *     converged) and is answered as "already current" rather than as an error.
 *
 * Every dependency is a function rather than an Android type, so the whole decision above runs in
 * the JVM suite. The platform half — the package parser and the installer session — is
 * [AndroidInstaller], and it is what the emulator layer exercises.
 *
 * @param installed what the phone currently has of that package, or null when it has none. Null is
 * a state and not a failure: it is every first install of a managed app.
 */
class AppUpdater(
    private val info: () -> ApkInfo,
    private val open: (url: String) -> InputStream,
    private val staging: () -> File,
    private val identify: (File) -> ApkIdentity?,
    private val installed: (packageName: String) -> ApkIdentity?,
    /**
     * Puts the verified archive where the platform can install it, and hands back the one call that
     * does. **When the platform work happens is the caller's decision, and the two callers differ.**
     *
     * The self-update opens the installer session here, eagerly, so that everything able to throw —
     * a user restriction, a full disk — throws while there is still a command acknowledgement to
     * put the reason in; what it hands back is the commit, which kills the process. The managed-app
     * path does the opposite and hands back a thunk that does all of it, because its whole
     * platform half has to run inside the window where `no_install_apps` is lifted, and the download
     * that precedes it must not.
     *
     * A throw from here is a refusal with the platform's own words in it. A throw from the thunk is
     * the managed-app applier's to catch.
     */
    private val stage: (file: File, packageName: String) -> () -> Unit,
    private val log: (String) -> Unit = {},
) {

    fun update(): UpdateOutcome {
        val want = try {
            info()
        } catch (e: Exception) {
            return UpdateOutcome.Refused("the server did not say which build to install (${reason(e)})")
        }
        if (want.packageName.isBlank()) {
            return UpdateOutcome.Refused("the server did not say which application to install")
        }
        if (want.packageChecksum.isBlank()) {
            return UpdateOutcome.Refused("the server hosts no build of ${want.packageName} to install")
        }

        // Read before the download rather than after it, so that the comparison below can be made
        // without spending 13 MB of a child's mobile data to establish it.
        val current = installed(want.packageName)

        // **The check that makes an automatic update loop possible** (FR-15.6). Every other check in
        // this function is made on the archive, after the download, and has to be: a claim by the
        // server about what it hosts is not evidence about the bytes it served. This one is
        // different because it can only end the attempt *early* — a server claiming a version that
        // is not newer cannot cause an install, it can only cause this phone not to download. A
        // server that lies the other way gains nothing: the archive's real version code is checked
        // below and a downgrade is still refused there.
        if (want.versionCode > 0 && current != null && want.versionCode <= current.versionCode) {
            return UpdateOutcome.AlreadyCurrent(current)
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

            if (archive.packageName != want.packageName) {
                return UpdateOutcome.Refused(
                    "the download is ${archive.packageName} and this phone was told to install " +
                        "${want.packageName}; it would install alongside rather than replace"
                )
            }

            // Null is "nothing of this package is on the phone", which is every first install of a
            // managed app. The two checks below are about REPLACING something, so with nothing to
            // replace they have no question to answer and are skipped rather than given a
            // stand-in — an ApkIdentity of zeroes would compare its empty signer against a real one
            // and refuse every first install.
            if (current != null) {
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
            }

            log(
                "staged ${archive.packageName} ${archive.versionName} (build ${archive.versionCode}) over " +
                    (current?.versionName?.ifEmpty { "build ${current.versionCode}" } ?: "nothing")
            )
            // The staged file outlives this function on purpose: the installer may read it during
            // the commit, which happens after the acknowledgement. It is deleted by the next update.
            //
            // **This is the last thing that can be reported on.** Whatever the caller does here —
            // open a session now, or defer the whole of it — a throw from it is a refusal with a
            // reason, and past this line there is nothing left that can produce one: the commit
            // ends the process. Before this call was made here, a platform refusal happened after
            // the acknowledgement had already said "installing now", and was logged to a log on the
            // phone and to nothing else.
            val commit = try {
                stage(file, want.packageName)
            } catch (e: Exception) {
                return UpdateOutcome.Refused("the platform refused to stage this install (${reason(e)})")
            }

            // fromVersionCode is 0 for a first install, which is the same number the server already
            // uses for "never reported" — there is no build to have come from, and inventing one
            // would put a version in the audit trail that was never on the phone.
            return UpdateOutcome.Staged(archive, current?.versionCode ?: 0L, commit)
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
