package io.github.helios57.familyguard

import io.github.helios57.familyguard.policy.DpmRestrictionGateway
import java.io.File
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * Nine properties of the app's shape that no runtime test can reach.
 *
 * A wrong component name, an unguarded exported component, a call to a wipe API and a drain moved
 * inside the sync lock are all things that compile, install, and behave perfectly on a device that
 * is not provisioned — which is every device in CI. They are checked by reading the sources, because
 * the sources are the only evidence available before someone flashes a phone.
 *
 * Every check here starts by proving it read something. A scan that resolves the wrong directory
 * finds no violations, which is byte-identical to a clean result.
 */
class ManifestAndPlatformCallsTest {

    private val main: File = sequenceOf("src/main", "app/src/main", "android-dpc/app/src/main")
        .map(::File)
        .firstOrNull { it.isDirectory }
        ?: throw AssertionError(
            "could not find the main source set from ${File(".").absolutePath}; this test would " +
                "otherwise pass by scanning nothing",
        )

    private val manifestText: String = File(main, "AndroidManifest.xml").readText()

    /**
     * The manifest that ships, not the one that is written. Library manifests merge into this app's
     * and bring components of their own — the debug variant declares seven where `src/main`
     * declares five — so an exported-surface check over the source file is a check of the half
     * nobody would have got wrong.
     */
    private val mergedText: String =
        javaClass.getResourceAsStream("/MergedAndroidManifest.xml")?.bufferedReader()?.readText()
            ?: throw AssertionError(
                "the merged manifest is not on the test classpath; the exported-surface check " +
                    "would otherwise pass by reading nothing",
            )

    /** The manifest with comments removed, so prose about `exported` cannot be read as an attribute. */
    private val manifest: String = manifestText.replace(Regex("<!--.*?-->", RegexOption.DOT_MATCHES_ALL), "")

    private val merged: String = mergedText.replace(Regex("<!--.*?-->", RegexOption.DOT_MATCHES_ALL), "")

    private val sources: List<File> =
        File(main, "kotlin").walkTopDown().filter { it.isFile && it.extension == "kt" }.toList()

    /**
     * Kotlin source with its comments removed.
     *
     * Every scan below looks for the presence or absence of a token in the code, and this file's own
     * KDoc is the counter-example for why the comments have to go first: it names
     * `ACTION_PACKAGE_ADDED` and `DevicePolicyManager::class.java` in prose, explaining why they
     * belong where they are. A check that read that prose would stay green after the code it
     * describes was deleted — the comment outliving the call is the normal way that happens.
     *
     * `//` preceded by a colon is left alone so a URL in a string is not truncated.
     */
    private fun code(file: File): String = file.readText()
        .replace(Regex("""/\*.*?\*/""", RegexOption.DOT_MATCHES_ALL), "")
        .replace(Regex("""(?<!:)//[^\n]*"""), "")

    @Test
    fun `the scans have something to scan`() {
        assertTrue("the manifest did not parse as one", manifestText.contains("<manifest"))
        assertTrue("the merged manifest did not parse as one", mergedText.contains("<manifest"))
        assertTrue(
            "the merged manifest has no more components than the source one, so it is probably " +
                "the source one",
            componentsIn(merged).size > componentsIn(manifest).size,
        )
        assertTrue("only ${sources.size} Kotlin sources were found", sources.size >= 5)
        assertTrue(
            "no source mentions addUserRestriction, so the platform-call scan is reading the " +
                "wrong files",
            sources.any { it.readText().contains("addUserRestriction") },
        )
    }

    /**
     * `DpmRestrictionGateway` names the admin receiver as a string to keep the `policy` package free
     * of a dependency on `admin`. A typo there produces a `ComponentName` the platform does not
     * recognise, so every `addUserRestriction` throws `SecurityException` on a device that is
     * correctly provisioned — the least diagnosable failure in the app.
     */
    @Test
    fun `the manifest and the gateway name the same admin receiver`() {
        val declared = Regex("""<receiver[^>]*android:name="(\.admin\.[A-Za-z]+)"""")
            .find(manifest)
            ?.groupValues
            ?.get(1)
            ?: throw AssertionError("the manifest declares no receiver under .admin")
        assertEquals(
            "the gateway's ADMIN_RECEIVER must be the class the manifest declares",
            "io.github.helios57.familyguard$declared",
            DpmRestrictionGateway.ADMIN_RECEIVER,
        )
    }

    /**
     * The whole inter-process surface of this app, guarded (FR-2.4).
     *
     * Everything this app exports is exported because the *system* calls it, and a permission is
     * what keeps "exported" from meaning "reachable by any app on the phone". Only signature-level
     * permissions qualify: a `normal` or `dangerous` one can be held by anything that asks.
     *
     * The allowlist is by permission rather than by component, so adding a component is free and
     * adding an unguarded one is not. Each entry says who holds it and why that is acceptable.
     *
     * **[UNGUARDED_EXPORTS] is the one exception, and it is by component name.** A MAIN/LAUNCHER
     * activity cannot carry a permission — the launcher does not hold one, so a guarded launcher
     * entry is an entry nobody can open — and the recovery screen exists precisely for the case
     * where every other route in is unavailable. Widening the permission allowlist to let it through
     * would have let *any* future unguarded component through with it, which is why the exception is
     * a name. What replaces the permission is asserted here and in
     * [the recovery activity trusts nothing from the intent that started it]: the component accepts
     * MAIN/LAUNCHER and nothing else, and its code reads no part of the intent.
     */
    @Test
    fun `every exported component in the shipped manifest is guarded by a signature permission`() {
        val allowed = mapOf(
            // Signature-level, held by the platform. This is how the device-policy service reaches
            // the admin receiver, the admin service and the two provisioning activities.
            "android.permission.BIND_DEVICE_ADMIN" to "platform only",
            // Signature|privileged. androidx.profileinstaller's receiver, reachable from adb and the
            // installer and nothing else. It arrives through a transitive dependency rather than
            // being asked for; it is left in place because removing a library's own initialisation
            // is the kind of change that only misbehaves on a real phone.
            "android.permission.DUMP" to "shell and privileged apps only",
        )

        val components = componentsIn(merged)
        assertTrue("no components were found in the merged manifest", components.size >= 6)

        val exported = components.filter { it.contains("""android:exported="true"""") }
        assertTrue(
            "nothing in the merged manifest is exported, so this test proves nothing about the guard",
            exported.isNotEmpty(),
        )
        for (component in exported) {
            val name = Regex("""android:name="([^"]+)"""").find(component)?.groupValues?.get(1)
            if (name in UNGUARDED_EXPORTS) continue
            val permission = Regex("""android:permission="([^"]+)"""").find(component)?.groupValues?.get(1)
            assertTrue(
                "an exported component is guarded by ${permission ?: "nothing"}, which is not in " +
                    "the allowlist ${allowed.keys}: ${component.take(140)}",
                permission in allowed,
            )
        }

        // The exception must still be *there*. An unguarded-exports list naming a component that no
        // longer exists is a hole held open for whatever is added next, and it would never go red on
        // its own: every other assertion here is about components that are present.
        for ((name, _) in UNGUARDED_EXPORTS) {
            assertTrue(
                "$name is listed as a permitted unguarded export but is not in the merged manifest; " +
                    "remove the entry rather than leaving the exception open",
                exported.any { it.contains("""android:name="$name"""") },
            )
        }

        // And it must be reachable *only* as a launcher entry. An extra action or a data scheme on
        // this component turns "the launcher can start it" into "anything that can build that intent
        // can start it, with a payload" — which is the whole reason a permission was acceptable to
        // drop in the first place.
        for ((name, _) in UNGUARDED_EXPORTS) {
            val element = elementNamed(merged, name)
            val actions = Regex("""<action[^>]*android:name="([^"]+)"""")
                .findAll(element).map { it.groupValues[1] }.toSet()
            val categories = Regex("""<category[^>]*android:name="([^"]+)"""")
                .findAll(element).map { it.groupValues[1] }.toSet()
            assertEquals(
                "$name answers to more than the launcher",
                setOf("android.intent.action.MAIN"),
                actions,
            )
            assertEquals(
                "$name declares a category other than LAUNCHER",
                setOf("android.intent.category.LAUNCHER"),
                categories,
            )
            assertTrue(
                "$name declares a <data> element, so it can be started with a payload",
                !element.contains("<data"),
            )
        }
        // Explicitness, not safety: measured, AGP's own merger already refuses to build a component
        // that has an intent-filter and no `android:exported`, and one without a filter defaults to
        // false. So this assertion catches nothing the toolchain would have let through — it is
        // here to keep the manifest readable as a list of decisions, and it is recorded as such
        // rather than counted as a second line of defence.
        assertTrue(
            "a component leaves android:exported implicit; state it either way",
            components.all { it.contains("android:exported=") },
        )
    }

    private fun componentsIn(xml: String): List<String> =
        Regex("""<(activity|service|receiver|provider)\b[^>]*>""").findAll(xml).map { it.value }.toList()

    /**
     * One component element and everything inside it, so its intent filters can be read.
     *
     * Found from the `android:name` outwards rather than by parsing, because [componentsIn] already
     * establishes that the opening tags are findable this way and a second parser would be a second
     * thing to be wrong. It throws rather than returning empty on a miss: an element that was not
     * found must not read as an element with no intent filters.
     */
    private fun elementNamed(xml: String, name: String): String {
        val at = xml.indexOf("""android:name="$name"""")
        if (at < 0) throw AssertionError("$name is not in the manifest, so its filters cannot be read")
        val open = xml.lastIndexOf('<', at)
        val tag = Regex("""<(\w+)""").find(xml.substring(open))?.groupValues?.get(1)
            ?: throw AssertionError("no element tag before $name")
        val openEnd = xml.indexOf('>', at)
        if (openEnd < 0) throw AssertionError("$name's <$tag> opening tag never ends")
        // A self-closing element has no children. Searching on for `</tag>` would run past it into
        // the *next* one and read that element's intent filters as this one's — a component with no
        // filters at all would then pass or fail on somebody else's.
        if (xml[openEnd - 1] == '/') return xml.substring(open, openEnd)
        val close = xml.indexOf("</$tag>", openEnd)
        if (close < 0) throw AssertionError("$name's <$tag> element never closes")
        return xml.substring(open, close)
    }

    /**
     * The compensating half of [UNGUARDED_EXPORTS]: the one component any app on the phone can
     * start must take nothing from whoever started it.
     *
     * The recovery screen releases enforcement. It is protected by a code and a persisted lockout,
     * both read from encrypted storage — and every one of those protections is bypassable by a
     * single line that trusts the caller. `if (intent.getBooleanExtra("skip_lockout", false))` is
     * two words longer than the code without it, would be added by somebody making the screen easier
     * to test, and would turn a launcher entry into an unlock API callable by any app on the phone.
     * There is no runtime test that reaches it, because the bypass only fires for a caller that
     * passes the extra.
     *
     * So the guard is absence, checked in the source: this file names no intent at all. Not
     * `getIntent`, not `intent.extras`, not `intent.action` — the token itself. That is stricter
     * than the property being defended, deliberately: "reads no *interesting* part of the intent"
     * needs a list of the interesting parts, and the list is exactly what nobody updates.
     */
    @Test
    fun `the recovery activity trusts nothing from the intent that started it`() {
        val activity = sources.singleOrNull { it.name == "RecoveryActivity.kt" }
            ?: throw AssertionError(
                "RecoveryActivity.kt was not found in the main source set; the escape hatch is the " +
                    "one exported component with no permission, and this is the guard that replaces " +
                    "it — a missing file must not read as a clean scan",
            )
        val text = code(activity)

        // Calibration, both halves, on text this test controls: a reader that cannot see the bypass
        // reports the real file clean for the same reason it reports every file clean.
        val withTheBug = """
            override fun onCreate(savedInstanceState: Bundle?) {
                if (intent.getBooleanExtra("skip_lockout", false)) release()
            }
        """
        assertTrue(
            "the reader does not notice a source that DOES read the intent, so its answer on the " +
                "real file means nothing",
            readsTheIntent(withTheBug),
        )
        assertTrue(
            "the reader flags a source that never mentions an intent, so it cannot discriminate",
            !readsTheIntent(withTheBug.replace("intent.getBooleanExtra(\"skip_lockout\", false)", "false")),
        )

        // And that the file is the screen it is supposed to be: "reads no intent" is trivially true
        // of an empty file, and of one whose submit path was deleted.
        assertTrue(
            "RecoveryActivity no longer submits anything, so this guard is watching a screen that " +
                "does nothing",
            text.contains("submit(") && text.contains("androidRecoveryController"),
        )
        assertTrue(
            "RecoveryActivity starts a service or another component; starting ConnectionService " +
                "here syncs, and a sync that reaches the server ends the recovery it was just given",
            !Regex("""\bstart(Activity|Service|ForegroundService)\b""").containsMatchIn(text),
        )

        assertTrue(
            "RecoveryActivity reads the intent that started it. It is exported with no permission " +
                "(see UNGUARDED_EXPORTS), so anything it takes from the caller is an input any app " +
                "on this phone controls",
            !readsTheIntent(text),
        )
    }

    /**
     * The status block (FR-13.4) cannot put the device token on screen, because it never reads it.
     *
     * `RecoveryActivity` is the launcher entry and is exported with no permission, so its screen is
     * readable by whoever is holding the phone — the child it manages, most of all. `deviceId` is
     * fine there and the parent needs it; `deviceToken` sits one field away on the same
     * [io.github.helios57.familyguard.enroll.Credentials] object and authenticates as this device. Anyone who
     * read it off the screen could report fabricated usage, pull the family's policy and heartbeat
     * as a phone in a drawer.
     *
     * `DeviceStatusTest` asserts that no rendered line *contains* a token, which is the runtime
     * half; it can only ever check the token it knows about. This is the half that holds for every
     * token: the three files that build and draw the screen do not name the field at all, so there
     * is no channel for one to arrive through.
     */
    @Test
    fun `nothing that builds the status screen reads the device token`() {
        val names = listOf("DeviceStatus.kt", "AndroidDeviceStatus.kt", "RecoveryActivity.kt")
        val screen = names.map { name ->
            sources.singleOrNull { it.name == name }
                ?: throw AssertionError(
                    "$name was not found in the main source set; a missing file scans clean, which " +
                        "is exactly what this guard must not accept",
                )
        }

        // Calibration on text this test owns. A reader that cannot see the field would report the
        // real files clean for the same reason it reports an empty file clean.
        val withTheLeak = """val line = StatusLine("Token", credentials.deviceToken, StatusLevel.OK)"""
        assertTrue(
            "the reader does not notice a source that DOES read the token, so its answer on the " +
                "real files means nothing",
            readsTheToken(withTheLeak),
        )
        assertTrue(
            "the reader flags a source that reads only the device id, so it cannot tell the two " +
                "fields apart — and the id is what the screen is supposed to show",
            !readsTheToken(withTheLeak.replace("deviceToken", "deviceId")),
        )

        // And that the files are still the ones that build the screen: "does not read the token" is
        // trivially true of a file that builds nothing.
        val gatherer = code(screen.single { it.name == "AndroidDeviceStatus.kt" })
        assertTrue(
            "AndroidDeviceStatus no longer reads the credential at all, so this guard is watching " +
                "a gatherer that gathers nothing",
            gatherer.contains("EncryptedCredentialStore") && gatherer.contains("deviceId"),
        )

        for (file in screen) {
            assertTrue(
                "${file.name} names the device token. It would reach a screen anyone holding the " +
                    "phone can read, and that token authenticates as this device",
                !readsTheToken(code(file)),
            )
        }
    }

    /** The credential field, not the word — `token = { … }` is an ApiClient lambda, not a leak. */
    private fun readsTheToken(code: String): Boolean =
        Regex("""\bdeviceToken\b""").containsMatchIn(code)

    /** Any mention of an intent at all — see the calling test for why the token, not the accessor. */
    private fun readsTheIntent(code: String): Boolean =
        Regex("""(?i)\bintent\b""").containsMatchIn(code)

    /**
     * FR-2.3 / NFR-6, checked at the only place it can be: the source.
     *
     * No command in this product wipes a phone and none sets factory-reset protection. A call to
     * either would be indistinguishable from working code until the day it ran, and by then the
     * device is either wiped or permanently tied to a Google account the family may not control.
     */
    @Test
    fun `nothing calls a platform API that could wipe the device or block resetting it`() {
        val forbidden = listOf(
            "setFactoryResetProtectionPolicy",
            "FactoryResetProtectionPolicy",
            "wipeData",
            "wipeDevice",
        )
        val found = mutableListOf<String>()
        for (file in sources) {
            val text = file.readText()
            for (call in forbidden) {
                if (text.contains(call)) found += "${file.path}: $call"
            }
        }
        assertTrue("forbidden platform calls: $found", found.isEmpty())
    }

    /**
     * The command drain never runs while the sync lock is held (FR-9).
     *
     * `syncLock` is a `Mutex`, and a `Mutex` is not reentrant. Four of the eight command handlers
     * re-sync — `SYNC_POLICY` and the three that only flip a server-side setting — so a drain moved
     * inside the lock would take it a second time through `runSync` and hang there. Nothing would go
     * red: the service keeps its notification, the stream stays open, the console shows a device that
     * is online, and every command from the first `SYNC_POLICY` onwards sits DELIVERED and unanswered
     * forever. `ConnectionService` is Android, so there is no runtime test that can reach this; the
     * source is the only evidence, and the reader below is calibrated against a file that has the bug
     * before its clean answer on the real one is allowed to mean anything.
     */
    @Test
    fun `the command drain runs outside the sync lock`() {
        val service = File(main, "kotlin/io/github/helios57/familyguard/sync/ConnectionService.kt")
        assertTrue("${service.path} is not where the connection loop lives any more", service.isFile)
        val text = code(service)

        // Calibration, both halves, on text this test controls. A reader that cannot see the defect
        // reports the real file clean for the same reason it reports everything clean.
        val withTheBug = """
            private suspend fun tick() {
                syncLock.withLock {
                    val t = runSync()
                    if (t.pending > 0) drain(commands, t.pending, "wake")
                }
            }
        """
        assertTrue(
            "the reader does not notice a drain that IS inside the lock, so its answer on the real " +
                "file means nothing",
            drainsInsideTheLock(withTheBug).isNotEmpty(),
        )
        assertTrue(
            "the reader flags a drain that is outside the lock, so it cannot discriminate",
            drainsInsideTheLock(withTheBug.replace("if (t.pending > 0) drain(commands, t.pending, \"wake\")", ""))
                .isEmpty(),
        )

        // And that the token exists at all in the real file: "no drain inside the lock" is trivially
        // true of a file with no drain in it, which is what deleting the queue would look like.
        assertTrue(
            "${service.path} calls no drain at all, so this guard is watching nothing",
            Regex("(?i)drain\\(").containsMatchIn(text),
        )
        assertTrue(
            "no syncLock.withLock block was found, so the extraction is reading the wrong shape",
            text.contains("syncLock.withLock"),
        )

        assertEquals(
            "a command drain runs with syncLock held; the four re-syncing handlers deadlock there",
            emptyList<String>(),
            drainsInsideTheLock(text),
        )
    }

    /** Every line inside a `syncLock.withLock { … }` block that calls a drain. */
    private fun drainsInsideTheLock(code: String): List<String> {
        val found = mutableListOf<String>()
        var from = 0
        while (true) {
            val at = code.indexOf("syncLock.withLock", from)
            if (at < 0) return found
            val open = code.indexOf('{', at)
            if (open < 0) throw AssertionError("a syncLock.withLock with no block; the reader is lost")
            var depth = 0
            var end = open
            while (end < code.length) {
                if (code[end] == '{') depth++
                if (code[end] == '}' && --depth == 0) break
                end++
            }
            if (depth != 0) {
                throw AssertionError("the braces from offset $open never balanced; the reader is lost")
            }
            found += Regex("(?i)^.*drain\\(.*$", RegexOption.MULTILINE)
                .findAll(code.substring(open, end))
                .map { it.value.trim() }
            from = end
        }
    }

    /**
     * The permission list, held to exactly what the code uses, in both directions.
     *
     * An *added* permission is scope creep, and on a device-owner app scope creep is not a style
     * question — this app runs with the platform's trust and every permission it holds is one an
     * attacker who reaches it also holds. A *removed* one is worse, because it fails silently:
     * dropping `QUERY_ALL_PACKAGES` leaves `getInstalledApplications` returning a filtered list, so
     * every blocked package reads "not installed", every sync reports clean, and the child keeps
     * using the app. Nothing goes red anywhere; the console just becomes fiction.
     *
     * Checked against the *merged* manifest, which is what ships. A dependency can contribute a
     * `uses-permission` the source manifest never mentions, and that is how a permission arrives
     * without anyone deciding to add it — the last entry below is exactly that case.
     */
    @Test
    fun `the permissions the shipped app asks for are exactly the ones it needs`() {
        val needed = mapOf(
            // Re-apply the baseline after a reboot: BootReceiver.
            "android.permission.RECEIVE_BOOT_COMPLETED" to "boot re-application",
            // The control-plane connection.
            "android.permission.INTERNET" to "enrollment, policy, heartbeat, event stream",
            // Read-only, for the heartbeat's wifi/cellular field.
            "android.permission.ACCESS_NETWORK_STATE" to "heartbeat connectivity",
            // Android 37 drops an app's packets to a local address without this, silently. A
            // self-hosted control plane on the family's own LAN is the case it costs.
            "android.permission.ACCESS_LOCAL_NETWORK" to "a control plane on the family's own network",
            // The connection is a foreground service so "lock now" locks now.
            "android.permission.FOREGROUND_SERVICE" to "ConnectionService",
            "android.permission.FOREGROUND_SERVICE_SPECIAL_USE" to "its specialUse type",
            // Granted to itself as device owner; without it the service runs with no notification,
            // which removes the child's disclosure that the phone is managed.
            "android.permission.POST_NOTIFICATIONS" to "the foreground-service notification",
            // See the manifest's own comment. Removing this is the silent failure described above.
            "android.permission.QUERY_ALL_PACKAGES" to "seeing the packages to suspend",
            // Declared so the appop can be granted at all; a device owner cannot grant it itself.
            // Without the declaration this app never appears in Settings' usage-access list, screen
            // time reads as unmeasurable forever, and the daily limit can never be reached.
            "android.permission.PACKAGE_USAGE_STATS" to "measuring per-package foreground time",
            // The wake-up that makes a bedtime start on a phone lying face down. Checked at run
            // time rather than assumed: an ungranted appop degrades the alarm to an inexact one,
            // which is why this is a permission whose absence costs precision, not the feature.
            "android.permission.SCHEDULE_EXACT_ALARM" to "the enforcement wake-up",
            // The siren's vibration (FR-9). A lost phone is usually a silenced phone, so this is the
            // half of "make it findable" that survives a muted ringer.
            "android.permission.VIBRATE" to "the find-my-phone siren",
            // "Locate now" (FR-9), granted to itself as device owner. COARSE is declared alongside
            // FINE because Android 12 answers a FINE-only request with COARSE when the user picks
            // approximate location, and an app that never declared COARSE then gets nothing at all.
            // BACKGROUND because the command arrives while the phone is in a pocket and this app has
            // no UI to be in front.
            "android.permission.ACCESS_FINE_LOCATION" to "the one-shot position fix",
            "android.permission.ACCESS_COARSE_LOCATION" to "its approximate fallback",
            "android.permission.ACCESS_BACKGROUND_LOCATION" to "locating a phone nobody is holding",
            // Installing the DPC the control plane hosts, over this app (FR-15). A device owner is
            // exempt from the user prompt but not from the declaration: without it the commit is
            // refused before any prompt would appear. The scope is narrow by construction — the only
            // APK this app installs is one whose checksum its own server published and whose signing
            // certificate equals this app's own.
            "android.permission.REQUEST_INSTALL_PACKAGES" to "installing the DPC over itself",
            // The other half of that, and the half whose absence is silent. Holding
            // REQUEST_INSTALL_PACKAGES is what makes an unspecified session default to
            // USER_ACTION_REQUIRED, so without this declaration the platform answers every commit
            // with STATUS_PENDING_USER_ACTION and installs nothing. Normal-level, API 31+, and
            // exercised in `an installer that could be asked for a tap declares the exemption`.
            "android.permission.UPDATE_PACKAGES_WITHOUT_USER_ACTION" to "installing without a tap",
            // Not authored here: androidx.core injects it, and a matching signature-level
            // <permission> declaration, because ConnectionService registers the install watcher with
            // ContextCompat.RECEIVER_NOT_EXPORTED. It is scoped to this app's own package name and
            // holdable only by something signed with this key, so it grants nothing outside the app
            // — but it ships, so it is listed rather than discovered later.
            "io.github.helios57.familyguard.DYNAMIC_RECEIVER_NOT_EXPORTED_PERMISSION" to
                "androidx.core, for the non-exported install watcher",
        )

        val shipped = usesPermissionsIn(merged)
        assertTrue(
            "the merged manifest declares no permissions at all, so this test is comparing " +
                "against nothing",
            shipped.isNotEmpty(),
        )
        assertEquals(
            "the permissions the shipped app asks for are not the ones this test knows a reason " +
                "for; added: ${shipped - needed.keys}, gone: ${needed.keys - shipped}",
            needed.keys,
            shipped,
        )

        // The `android.permission.*` ones must come from this project's own manifest. Without this
        // half, a library contributing one of them would keep the set equal above while the source
        // manifest lost the declaration — and `QUERY_ALL_PACKAGES` arriving from a dependency is a
        // permission whose lifetime is that dependency's next version bump.
        val authored = usesPermissionsIn(manifest)
        assertEquals(
            "the source manifest no longer authors the permissions this app depends on directly",
            needed.keys.filter { it.startsWith("android.permission.") }.toSet(),
            authored,
        )
    }

    /**
     * The pairing that made FR-15 fail silently on the pilot phone, held together in one test
     * because either half alone is a working-looking app that never installs anything.
     *
     * Measured 2026-09-06: the DPC declared `REQUEST_INSTALL_PACKAGES` and never called
     * `setRequireUserAction`. That declaration is exactly what makes the documented default
     * `USER_ACTION_UNSPECIFIED` behave as `USER_ACTION_REQUIRED`, so every commit was answered
     * `STATUS_PENDING_USER_ACTION` — a request for a tap, delivered to an app with no UI, on a
     * child's phone. The command was acknowledged as "installing now", the phone kept heartbeating,
     * and the version never moved.
     *
     * There is no runtime test for this: an unprovisioned device, which is every device in CI,
     * refuses the session for a different reason and reports the same nothing.
     */
    @Test
    fun `an installer that could be asked for a tap declares the exemption and asks for none`() {
        val installer = sources.single { it.name == "AndroidInstaller.kt" }
        val text = code(installer)
        val sessionParams = "PackageInstaller.SessionParams(PackageInstaller.SessionParams.MODE_FULL_INSTALL)"

        // The positive controls. Each assertion below is an *absence* check, and an absence found
        // in a file that was not read has the same shape as a clean result.
        assertTrue(
            "AndroidInstaller.kt does not construct an installer session, so this test is " +
                "scanning the wrong file",
            text.contains(sessionParams),
        )
        assertTrue(
            "the source manifest no longer asks to install packages, so the pairing below is moot " +
                "and this test would pass by checking nothing",
            usesPermissionsIn(manifest).contains("android.permission.REQUEST_INSTALL_PACKAGES"),
        )

        assertTrue(
            "REQUEST_INSTALL_PACKAGES is declared without UPDATE_PACKAGES_WITHOUT_USER_ACTION; " +
                "the platform treats every session as USER_ACTION_REQUIRED and installs nothing",
            usesPermissionsIn(manifest).contains("android.permission.UPDATE_PACKAGES_WITHOUT_USER_ACTION"),
        )
        assertTrue(
            "no session sets USER_ACTION_NOT_REQUIRED; an unspecified session from an installer " +
                "holding REQUEST_INSTALL_PACKAGES asks for a tap nobody is there to give",
            text.contains("setRequireUserAction(PackageInstaller.SessionParams.USER_ACTION_NOT_REQUIRED)"),
        )
        assertEquals(
            "the self-update path and the managed-app path must build their session parameters in " +
                "one place; two constructions are two sets of parameters that drift, and the one " +
                "that drifts is the one nobody watches",
            1,
            text.split(sessionParams).size - 1,
        )
    }

    /**
     * The parent's button and the timer reach the same updater (FR-15.6).
     *
     * They are two entry points into one feature, and the failure mode is not that one of them
     * breaks — it is that one of them is *fixed*. The command path is the one a person exercises
     * when they are watching, so it is the one that gets the next correction; the timer path is the
     * one that runs on 4 500 phones at 03:00 and nobody looks at. Two constructions of [AppUpdater]
     * in this file is that split, and it has no symptom until a fleet stops updating.
     *
     * Counted rather than inspected: this reads as "how many places decide what an update is", and
     * the answer has to be one.
     */
    @Test
    fun `the update button and the update timer are one wiring, not two`() {
        val service = sources.single { it.name == "ConnectionService.kt" }
        val text = code(service)

        // The positive control: both entry points are still in this file at all. Without it, a
        // rename would make every count below zero and the test would report agreement.
        assertTrue(
            "ConnectionService.kt no longer runs an automatic update check, so this test is " +
                "scanning a file that has stopped carrying the thing it is about",
            text.contains("private suspend fun updateLoop("),
        )
        assertEquals(
            "the self-update is built in ${text.split("selfUpdater(api, policy)").size - 1} places; " +
                "it must be exactly the two callers — the parent's command and the timer — both " +
                "going through one builder",
            2,
            text.split("selfUpdater(api, policy)").size - 1,
        )
        assertEquals(
            "there is more than one builder, so a fix applied to one of them leaves the other on " +
                "the old behaviour",
            1,
            text.split("private fun selfUpdater(").size - 1,
        )

        // The loop is launched into the connection's scope and cancelled with it. A coroutine that
        // outlives the connection is a second updater running against a stale ApiClient, and the
        // symptom is an update that reports failures from a session that no longer exists.
        assertTrue(
            "the update loop is not cancelled when the connection ends",
            text.contains("updates.cancel()"),
        )
    }

    private fun usesPermissionsIn(xml: String): Set<String> =
        Regex("""<uses-permission[^>]*android:name="([^"]+)"""")
            .findAll(xml)
            .map { it.groupValues[1] }
            .toSet()

    /**
     * Every call into the device-policy service goes through a file that was written to check what
     * the platform actually did.
     *
     * `setPackagesSuspended`, `addUserRestriction`, `setApplicationRestrictions` and
     * `setGlobalPrivateDnsModeSpecifiedHost` are *requests*. The platform accepting one is not the
     * platform applying it, and accepted-and-not-applied is the dominant failure shape on a real
     * phone — it produces a console showing a configured restriction over a device enforcing
     * nothing. The read-back that catches it lives in the gateways, so a `DevicePolicyManager`
     * obtained anywhere else is a call whose result nobody checks.
     *
     * Detected by who *holds* a handle rather than by the call names: a list of forbidden method
     * names only ever catches the methods already thought of, while there is no way to call the
     * service at all without first getting one of these.
     */
    @Test
    fun `only the files written to check the platform hold a device-policy handle`() {
        val allowed = mapOf(
            // The four managers and their gateways. Each one reads its own state back and reports
            // "accepted, and the device reports otherwise" as a failure.
            "io/github/helios57/familyguard/policy/DeviceOwnerPolicy.kt" to "the managers and their gateways",
            // User restrictions, split out so `policy` need not depend on `admin`.
            "io/github/helios57/familyguard/policy/DpmRestrictionGateway.kt" to "add/clear user restriction",
            // Automatic network time (FR-2.2). It is here for the same reason as the rest: the
            // setter returns void, so an OEM that ignores it is indistinguishable from one that
            // complied until the value is read back — and the difference is a clock a child can set
            // back an hour to walk around the daily quota.
            "io/github/helios57/familyguard/policy/DpmClockGateway.kt" to "automatic network time",
            // One call, on itself, at startup, for each runtime permission this app needs: nobody is
            // there to answer a dialog. It changes no policy the parent set and reads back through
            // the platform's own return value, which for this call is the grant state rather than an
            // acknowledgement of a request.
            "io/github/helios57/familyguard/sync/ConnectionService.kt" to "granting its own runtime permissions",
        )

        val kotlin = File(main, "kotlin")
        val holders = sources.filter { file ->
            val text = code(file)
            // Obtaining one, or being handed one. Constant access — `DevicePolicyManager.EXTRA_…` in
            // the provisioning activities — is deliberately not a match: a constant cannot change
            // anything on the device.
            text.contains("DevicePolicyManager::class.java") ||
                Regex(""":\s*DevicePolicyManager\b""").containsMatchIn(text)
        }.map { it.relativeTo(kotlin).path.replace(File.separatorChar, '/') }.toSet()

        assertTrue(
            "no file holds a device-policy handle, so this test has nothing to check and the " +
                "detector is reading the wrong sources",
            holders.isNotEmpty(),
        )
        assertEquals(
            "the set of files holding a device-policy handle changed; new: ${holders - allowed.keys}, " +
                "gone: ${allowed.keys - holders}",
            allowed.keys,
            holders,
        )
    }

    /**
     * The install watcher is registered at runtime, and must never be moved into the manifest.
     *
     * `ACTION_PACKAGE_ADDED` is an implicit broadcast. Since Android 8 a manifest-declared receiver
     * for one is simply never called, while a context-registered receiver is exempt. Moving this
     * into the manifest is the tidier-looking of the two, compiles, installs, and produces a phone
     * where a newly installed app is never suspended until the next poll — with nothing in any log
     * to say a broadcast was dropped, because none was sent.
     */
    @Test
    fun `the install watcher is registered at runtime, not declared in the manifest`() {
        val implicit = listOf("android.intent.action.PACKAGE_ADDED", "android.intent.action.PACKAGE_REPLACED")
        for (action in implicit) {
            assertTrue(
                "$action is declared in the merged manifest; Android 8+ never delivers an implicit " +
                    "broadcast to a manifest-declared receiver, so that receiver would never run",
                !merged.contains(action),
            )
        }
        val watcher = code(sources.single { it.name == "ConnectionService.kt" })
        assertTrue(
            "ConnectionService no longer registers a receiver for package installs, so a newly " +
                "installed app is only restrained at the next poll",
            watcher.contains("ACTION_PACKAGE_ADDED") &&
                watcher.contains("ACTION_PACKAGE_REPLACED") &&
                watcher.contains("registerReceiver"),
        )
    }

    /**
     * NFR-8, at the layer that binds on the device rather than at the server's end of the wire.
     *
     * `network_security_config.xml` is the second of the two mechanisms that keep this app's traffic
     * off cleartext, and the only one that catches an https→http redirect — `Enroller` refuses a
     * cleartext URL that arrives in a QR code, but a redirect is a URL nobody in this codebase ever
     * saw. It binds at runtime on a real phone, so nothing in the unit or e2e layers exercises it;
     * an `android:networkSecurityConfig` attribute lost in a manifest edit would take the whole
     * protection with it and change no test result anywhere.
     *
     * The debug overlay is checked against `Enroller.CLEARTEXT_HOSTS` because the two lists are the
     * one thing here that can drift: they are written in different languages, in different files,
     * and either one alone lets a cleartext control plane through. Read out of the source text
     * rather than off the constant — it is private, and a test is not a reason to widen it.
     */
    @Test
    fun `cleartext is refused by the shipping config, and the debug carve-out matches Enroller`() {
        assertTrue(
            "the shipped manifest no longer points at a network security config; cleartext and " +
                "user-added CAs would both be allowed again, and no other test would notice",
            merged.contains("""android:networkSecurityConfig="@xml/network_security_config""""),
        )

        val release = xmlWithoutComments(File(main, "res/xml/network_security_config.xml"))
        assertTrue(
            "the release network security config did not parse; this test would otherwise pass by " +
                "reading nothing",
            release.contains("<base-config"),
        )
        assertTrue(
            "the release config does not refuse cleartext",
            release.contains("""cleartextTrafficPermitted="false""""),
        )
        assertTrue(
            "the release config carves out a cleartext exemption; there is no host a shipped build " +
                "may reach in the clear",
            !release.contains("""cleartextTrafficPermitted="true""""),
        )
        assertTrue(
            "the release config trusts user-added CAs; the person with the strongest motive to " +
                "install a proxy CA is the person the policy applies to",
            release.contains("""src="system"""") && !release.contains("""src="user""""),
        )

        // The debug overlay ships in nothing a parent installs, but it is what gets exercised on a
        // bench — so it is held to the same shape, minus exactly the loopback names.
        val debugFile = File(main.parentFile, "debug/res/xml/network_security_config.xml")
        assertTrue("the debug network security overlay is missing at ${debugFile.path}", debugFile.isFile)
        val debug = xmlWithoutComments(debugFile)
        assertTrue(
            "the debug overlay trusts user-added CAs, so the configuration exercised on a bench is " +
                "not the one that ships and a TLS problem first appears on a child's phone",
            !debug.contains("""src="user""""),
        )
        assertTrue(
            "the debug overlay's base config no longer refuses cleartext, which makes the carve-out " +
                "below meaningless",
            debug.contains("""cleartextTrafficPermitted="false""""),
        )

        val exempt = Regex("""<domain[^>]*>([^<]+)</domain>""")
            .findAll(debug)
            .map { it.groupValues[1].trim() }
            .toSet()
        val enroller = code(sources.single { it.name == "Enroller.kt" })
        val allowed = Regex("""CLEARTEXT_HOSTS\s*=\s*setOf\(([^)]*)\)""")
            .find(enroller)
            ?.groupValues
            ?.get(1)
            ?.let { Regex(""""([^"]+)"""").findAll(it).map { m -> m.groupValues[1] }.toSet() }
            ?: throw AssertionError("Enroller no longer declares CLEARTEXT_HOSTS as a setOf literal")

        assertTrue(
            "neither list has any entries, so this comparison proves nothing",
            exempt.isNotEmpty() && allowed.isNotEmpty(),
        )
        assertEquals(
            "the debug cleartext carve-out and Enroller.CLEARTEXT_HOSTS have drifted; either one " +
                "alone lets a cleartext control plane through",
            allowed,
            exempt,
        )
    }

    private fun xmlWithoutComments(file: File): String =
        file.readText().replace(Regex("<!--.*?-->", RegexOption.DOT_MATCHES_ALL), "")

    private companion object {
        /**
         * Components that are exported with no permission, by name, each with what stands in for one.
         *
         * There is exactly one, and adding a second should be hard: this is the list of things any
         * app on the phone can start. An entry here is a claim that the component is safe when
         * reached by anything, and the claim has to be checked somewhere — see the two tests that
         * read it.
         */
        val UNGUARDED_EXPORTS = mapOf(
            "io.github.helios57.familyguard.recovery.RecoveryActivity" to
                "the launcher entry for the offline escape hatch (FR-12). A launcher cannot hold a " +
                    "permission, so a guarded MAIN/LAUNCHER activity is unreachable — which in this " +
                    "case means the way out of a locked phone is unreachable. It answers to " +
                    "MAIN/LAUNCHER only and reads nothing from the intent; both are asserted.",
        )
    }
}
