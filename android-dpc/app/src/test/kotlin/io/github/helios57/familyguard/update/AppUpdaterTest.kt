package io.github.helios57.familyguard.update

import java.io.ByteArrayInputStream
import java.io.File
import java.io.IOException
import java.io.InputStream
import java.security.MessageDigest
import java.util.Base64
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.rules.TemporaryFolder

/** The url-safe unpadded base64 SHA-256 the server publishes, computed the way the server does. */
private fun checksumOf(bytes: ByteArray): String =
    Base64.getUrlEncoder().withoutPadding()
        .encodeToString(MessageDigest.getInstance("SHA-256").digest(bytes))

private const val SIGNER = "b62cda948ad3a08ecb2af47d1617173db9bdaf3b31bb63b036ff91addb8a8e10"

/** The package under test throughout. Every archive and every install is about this one. */
private const val PACKAGE = "io.github.helios57.familyguard"

private fun installed(code: Long = 2, signer: String = SIGNER, pkg: String = PACKAGE) =
    ApkIdentity(packageName = pkg, versionCode = code, versionName = "0.1.$code", signerSha256 = signer)

/**
 * The updater with every dependency under the test's control.
 *
 * Written as a builder rather than as six positional lambdas at each call site because each test
 * changes exactly one of them, and a test that has to restate the other five is a test whose
 * *unchanged* parts drift out of agreement with the others.
 */
private class Harness(private val folder: TemporaryFolder) {
    var bytes: ByteArray = "a signed apk, near enough for this layer".toByteArray()
    var declaredSize: Long? = null
    var declaredChecksum: String? = null
    var url = "https://guard.example.com/dpc.apk"
    var infoFails: Exception? = null
    var downloadFails: Exception? = null
    var archive: ApkIdentity? = ApkIdentity(PACKAGE, 3, "0.1.2", SIGNER)

    /**
     * What the server *claims* it hosts, before anything is downloaded. 0 is an older control
     * plane that does not say — the default here, so that every test written before the claim
     * existed keeps exercising the download path it was written for.
     */
    var wantVersionCode: Long = 0

    /** What the platform does when the session cannot be opened: a user restriction, a full disk. */
    var stageFails: Exception? = null

    /** Null is "nothing of this package is on the phone" — every first install of a managed app. */
    var current: ApkIdentity? = installed()
    var wantPackage = PACKAGE
    var truncateTo: Int? = null

    /**
     * Serve this many bytes instead of [bytes], regardless of what the server declared. The point
     * of the field is a response that does not end where it said it would.
     */
    var overrunTo: Int? = null

    val installedFiles = mutableListOf<File>()
    val installedPackages = mutableListOf<String>()
    val askedAbout = mutableListOf<String>()
    val stagedFiles = mutableListOf<File>()

    /** How many times the body was opened. The unit of a wasted 13 MB on a child's mobile data. */
    var downloads = 0

    fun updater(): AppUpdater {
        val staged = File(folder.root, "staged.apk")
        return AppUpdater(
            info = {
                infoFails?.let { throw it }
                ApkInfo(
                    packageName = wantPackage,
                    url = url,
                    packageChecksum = declaredChecksum ?: checksumOf(bytes),
                    size = declaredSize ?: bytes.size.toLong(),
                    versionCode = wantVersionCode,
                )
            },
            open = { _ ->
                downloads += 1
                downloadFails?.let { throw it }
                val body = when {
                    overrunTo != null -> ByteArray(overrunTo!!) { bytes[it % bytes.size] }
                    truncateTo != null -> bytes.copyOfRange(0, truncateTo!!)
                    else -> bytes
                }
                ByteArrayInputStream(body) as InputStream
            },
            staging = { staged },
            identify = { archive },
            installed = { pkg -> askedAbout += pkg; current },
            stage = { file, pkg ->
                stageFails?.let { throw it }
                stagedFiles += file
                val commit = { installedFiles += file; installedPackages += pkg }
                commit
            },
        )
    }
}

class AppUpdaterTest {

    @get:Rule
    val folder = TemporaryFolder()

    @Test
    fun `stages a newer build and hands back a commit that has not run yet`() {
        val h = Harness(folder)
        val outcome = h.updater().update()

        val staged = outcome as? UpdateOutcome.Staged
            ?: throw AssertionError("expected a staged update, got $outcome")
        assertEquals(3L, staged.identity.versionCode)
        assertEquals(2L, staged.fromVersionCode)
        // The property the whole design rests on: committing kills the process, so the updater must
        // not have committed by the time it returns — the command has not been acknowledged yet.
        assertTrue(
            "the updater installed before returning; the acknowledgement for this command would " +
                "never be sent, and the console would show a phone that never answered",
            h.installedFiles.isEmpty(),
        )

        staged.commit()
        assertEquals(1, h.installedFiles.size)
        assertTrue("the file handed to the installer must be the one that was verified",
            h.installedFiles.single().readBytes().contentEquals(h.bytes))
    }

    @Test
    fun `answers already-current when the phone runs the build the server hosts`() {
        val h = Harness(folder)
        h.archive = ApkIdentity(PACKAGE, 2, "0.1.1", SIGNER)
        h.current = installed(code = 2)

        val outcome = h.updater().update()
        assertTrue("an equal version is not a failure: it is a parent pressing the button twice",
            outcome is UpdateOutcome.AlreadyCurrent)
        assertTrue(h.installedFiles.isEmpty())
    }

    @Test
    fun `refuses a downgrade rather than reporting an install that can never happen`() {
        val h = Harness(folder)
        h.archive = ApkIdentity(PACKAGE, 1, "0.1.0", SIGNER)
        h.current = installed(code = 5)

        val outcome = h.updater().update()
        val refused = outcome as? UpdateOutcome.Refused
            ?: throw AssertionError("expected a refusal, got $outcome")
        assertTrue("the reason must name both builds: $refused", refused.reason.contains("5"))
        assertTrue(h.installedFiles.isEmpty())
    }

    /**
     * The check that makes a hostile or misconfigured `apk-info` inert. The platform refuses this
     * too — from inside the installer, after the download, with a message the parent never sees.
     */
    @Test
    fun `refuses an APK signed by a different certificate`() {
        val h = Harness(folder)
        h.archive = ApkIdentity(PACKAGE, 9, "9.9.9", "0000000000000000000000000000000000000000000000000000000000000000")

        val refused = h.updater().update() as? UpdateOutcome.Refused
            ?: throw AssertionError("a differently-signed APK was not refused")
        assertTrue(refused.reason.contains("certificate"))
        assertTrue(h.installedFiles.isEmpty())
    }

    @Test
    fun `refuses a download that does not match the checksum the server published`() {
        val h = Harness(folder)
        h.declaredChecksum = checksumOf("some other build entirely".toByteArray())

        val refused = h.updater().update() as? UpdateOutcome.Refused
            ?: throw AssertionError("a mismatched download was not refused")
        assertTrue(refused.reason.contains("checksum"))
        assertTrue(h.installedFiles.isEmpty())
    }

    /**
     * A cut-off download is the ordinary failure on a phone, and the one that produces a file that
     * looks fine. It is refused on size before the checksum so the parent is told the download was
     * interrupted rather than that the file was wrong — different problem, different fix.
     */
    @Test
    fun `refuses a truncated download and says it was truncated`() {
        val h = Harness(folder)
        h.truncateTo = 10

        val refused = h.updater().update() as? UpdateOutcome.Refused
            ?: throw AssertionError("a truncated download was not refused")
        assertTrue("the reason must say how far it got: ${refused.reason}", refused.reason.contains("10 bytes"))
        assertFalse("a truncated download must not be reported as a checksum problem",
            refused.reason.contains("checksum"))
    }

    @Test
    fun `refuses when the server hosts something that is not a readable APK`() {
        val h = Harness(folder)
        h.archive = null

        val refused = h.updater().update() as? UpdateOutcome.Refused
            ?: throw AssertionError("an unparseable download was not refused")
        assertTrue(refused.reason.contains("APK"))
    }

    @Test
    fun `reports a server that hosts no build as a refusal, not as an install`() {
        val h = Harness(folder)
        h.declaredChecksum = ""

        val refused = h.updater().update() as? UpdateOutcome.Refused
            ?: throw AssertionError("an empty checksum was not refused")
        assertTrue(refused.reason.contains("no build of $PACKAGE"))
    }

    @Test
    fun `reports the transport's own reason when the metadata call fails`() {
        val h = Harness(folder)
        h.infoFails = IOException("connection reset")

        val refused = h.updater().update() as? UpdateOutcome.Refused
            ?: throw AssertionError("a failed apk-info call was not refused")
        assertTrue("the parent needs the transport's words: ${refused.reason}",
            refused.reason.contains("connection reset"))
    }

    @Test
    fun `reports the transport's own reason when the download fails`() {
        val h = Harness(folder)
        h.downloadFails = IOException("network is unreachable")

        val refused = h.updater().update() as? UpdateOutcome.Refused
            ?: throw AssertionError("a failed download was not refused")
        assertTrue(refused.reason.contains("network is unreachable"))
    }

    /**
     * The size comparison in `update` runs after the copy, so on its own it reports an overrun
     * rather than stopping one — the phone's storage is already gone by the time it speaks. This
     * asserts the copy itself gives up, and that what it wrote stayed bounded.
     */
    @Test
    fun `a response that does not end is abandoned instead of filling the phone`() {
        val h = Harness(folder)
        h.declaredSize = h.bytes.size.toLong()
        h.overrunTo = 16 * 1024 * 1024

        val refused = h.updater().update() as? UpdateOutcome.Refused
            ?: throw AssertionError("an endless download was not refused")
        assertTrue("says the download failed: ${refused.reason}", refused.reason.contains("abandoned"))

        val staged = File(folder.root, "staged.apk")
        assertTrue(
            "wrote ${staged.length()} bytes for a ${h.bytes.size}-byte build",
            staged.length() < 16 * 1024 * 1024,
        )
    }

    // ---- installing something that is not this app (FR-16.3) ---------------------------------

    /**
     * The first install of a managed app: nothing of that package is on the phone.
     *
     * `installed` returns null, and the two checks that are about *replacing* something must be
     * skipped rather than run against a stand-in. A zero-valued ApkIdentity would compare its empty
     * signer against a real one and refuse every first install — a feature that could never place
     * its first app, failing with a message about certificates.
     */
    @Test
    fun `installs a package the phone does not have yet`() {
        val h = Harness(folder)
        h.wantPackage = "ch.example.muplay"
        h.archive = ApkIdentity("ch.example.muplay", 7, "1.4.0", "aa".repeat(32))
        h.current = null

        val staged = h.updater().update() as? UpdateOutcome.Staged
            ?: throw AssertionError("a first install was refused")
        assertEquals(7L, staged.identity.versionCode)
        assertEquals(
            "a first install came from no build at all, and 0 is the number the server already " +
                "uses for that; any other value would put a version in the audit trail that was " +
                "never on the phone",
            0L, staged.fromVersionCode,
        )

        staged.commit()
        assertEquals(listOf("ch.example.muplay"), h.installedPackages)
        assertEquals(
            "the updater must ask the phone about the package it was told to install",
            listOf("ch.example.muplay"), h.askedAbout,
        )
    }

    /**
     * The check that separates "replace" from "install alongside".
     *
     * Neither the platform nor the file itself objects: installing a different package is a
     * perfectly successful install of something nobody asked for. On the self-update path that is
     * two device-policy apps on one phone, one of them unmanaged; on the managed-app path it is an
     * application on a child's phone that no parent chose.
     */
    @Test
    fun `refuses an archive that is a different package than the one asked for`() {
        val h = Harness(folder)
        h.wantPackage = "ch.example.muplay"
        h.archive = ApkIdentity("ch.example.something.else", 7, "1.4.0", SIGNER)
        h.current = null

        val refused = h.updater().update() as? UpdateOutcome.Refused
            ?: throw AssertionError("an archive of the wrong package was not refused")
        assertTrue(
            "the reason must name both packages: ${refused.reason}",
            refused.reason.contains("ch.example.something.else") && refused.reason.contains("ch.example.muplay"),
        )
        assertTrue(h.installedFiles.isEmpty())
    }

    /**
     * The negative control for the two skipped checks. "Not installed" must not switch off the
     * signer comparison for a package that IS installed — otherwise the whole of check 5 would be
     * one null away from never running.
     */
    @Test
    fun `an installed managed app is still held to the signer it already has`() {
        val h = Harness(folder)
        h.wantPackage = "ch.example.muplay"
        h.archive = ApkIdentity("ch.example.muplay", 8, "1.5.0", "aa".repeat(32))
        h.current = ApkIdentity("ch.example.muplay", 7, "1.4.0", "bb".repeat(32))

        val refused = h.updater().update() as? UpdateOutcome.Refused
            ?: throw AssertionError("a managed app signed by a different key was not refused")
        assertTrue(refused.reason.contains("certificate"))
        assertTrue(h.installedFiles.isEmpty())
    }

    @Test
    fun `refuses when the caller named no package at all`() {
        val h = Harness(folder)
        h.wantPackage = ""

        val refused = h.updater().update() as? UpdateOutcome.Refused
            ?: throw AssertionError("an empty package name was not refused")
        assertTrue(refused.reason.contains("which application"))
    }

    /**
     * The negative control for the size check. A server that declares no size still gets the
     * checksum — otherwise "size 0" would switch off the verification that matters.
     */
    @Test
    fun `an undeclared size still verifies the checksum`() {
        val h = Harness(folder)
        h.declaredSize = 0
        h.declaredChecksum = checksumOf("a different build".toByteArray())

        val refused = h.updater().update() as? UpdateOutcome.Refused
            ?: throw AssertionError("size 0 switched off the checksum check")
        assertTrue(refused.reason.contains("checksum"))
    }

    // ---- the pre-download check that makes an automatic loop affordable (FR-15.6) -------------

    /**
     * The only reason a fifteen-minute loop is acceptable on a child's phone: when the server has
     * said which build it hosts and it is not newer, the attempt costs one small request.
     */
    @Test
    fun `does not download at all when the server says it hosts the build this phone runs`() {
        val h = Harness(folder)
        h.wantVersionCode = 2
        h.current = installed(code = 2)

        val outcome = h.updater().update()
        assertTrue("an equal declared version is not a failure: $outcome", outcome is UpdateOutcome.AlreadyCurrent)
        assertEquals(
            "the whole point of the claim is that the 13 MB is never fetched",
            0, h.downloads,
        )
    }

    /**
     * The negative control. Every check above this one runs on the archive, so the claim must only
     * ever be able to END an attempt early — never to start one, and never to switch a check off.
     * Three ways the short-circuit must NOT fire, each of which would otherwise make it a silent
     * "no update, ever": an older control plane that sends no version code at all, a genuinely
     * newer build, and a phone that has nothing of this package installed.
     */
    @Test
    fun `downloads when the claim cannot answer the question`() {
        val undeclared = Harness(folder).also { it.wantVersionCode = 0 }
        assertTrue(undeclared.updater().update() is UpdateOutcome.Staged)
        assertEquals("an older server declares nothing, and the answer to that is to find out",
            1, undeclared.downloads)

        val newer = Harness(folder).also { it.wantVersionCode = 3; it.current = installed(code = 2) }
        assertTrue(newer.updater().update() is UpdateOutcome.Staged)
        assertEquals("a newer declared build must be fetched", 1, newer.downloads)

        // A first install has nothing to compare against, so the claim decides nothing. If this
        // ever short-circuits, a managed app could never be placed on a phone at all.
        val first = Harness(folder).also { it.wantVersionCode = 1; it.current = null }
        assertTrue(first.updater().update() is UpdateOutcome.Staged)
        assertEquals("nothing installed means nothing to be already-current with", 1, first.downloads)
    }

    // ---- a platform refusal has to be reportable (FR-15.7) -----------------------------------

    /**
     * Measured on the owner's phone, 2026-09-06: an update was downloaded, verified and handed to
     * Android, the command was acknowledged as "installing now", and nothing installed — the
     * platform had answered `STATUS_PENDING_USER_ACTION` and the answer went to a log on the phone.
     * Opening the session inside [AppUpdater.update] is what gives that refusal somewhere to go:
     * the outcome the parent reads, instead of a `Staged` that nobody can commit.
     */
    @Test
    fun `reports a platform refusal instead of a staged update nobody can commit`() {
        val h = Harness(folder)
        h.stageFails = SecurityException("Permission Denial: install not allowed for this user")

        val refused = h.updater().update() as? UpdateOutcome.Refused
            ?: throw AssertionError("a session the platform refused to open was reported as staged")
        assertTrue(
            "the parent needs the platform's own words: ${refused.reason}",
            refused.reason.contains("Permission Denial"),
        )
        assertTrue("nothing may be committed after a refusal", h.installedFiles.isEmpty())
    }

    /**
     * The negative control for the one above: with the platform willing, the same path stages
     * exactly once and still defers the commit. Without this, `stageFails` could be doing nothing
     * and the refusal test would pass on an updater that never staged anything at all.
     */
    @Test
    fun `stages exactly once when the platform accepts, and still defers the commit`() {
        val h = Harness(folder)

        val staged = h.updater().update() as? UpdateOutcome.Staged
            ?: throw AssertionError("a healthy update was not staged")
        assertEquals(1, h.stagedFiles.size)
        assertTrue("staging must not install", h.installedFiles.isEmpty())

        staged.commit()
        assertEquals(listOf(PACKAGE), h.installedPackages)
    }
}
