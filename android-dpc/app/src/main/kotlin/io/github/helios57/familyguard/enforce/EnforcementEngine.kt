package io.github.helios57.familyguard.enforce

import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import java.time.OffsetDateTime
import java.time.ZoneId
import java.time.ZonedDateTime
import java.time.format.DateTimeFormatter
import java.time.zone.ZoneRulesException

/**
 * The enforcement engine: a pure function from stored policy to the state the phone must make true.
 *
 * There are two of these — this one and `backend/internal/policy` in Go — and that is deliberate.
 * The phone has to keep enforcing bedtime and the daily quota with no network (FR-9), so it cannot
 * ask the server what to do; and the console has to show a parent what is in effect right now, so
 * the server cannot ask the phone. Two implementations of one rule set is the cost of that.
 *
 * The risk it creates is drift, and drift here is not a crash: it is a phone that unlocks at 06:00
 * while the console says 07:00, with nothing red anywhere. `vectors.json` is the defence — a single
 * hand-written description of what the rules mean, replayed by both engines. Neither side may edit
 * it to make its own implementation pass.
 *
 * Nothing in this file imports an Android API. That is what lets [EnforcementEngineVectorsTest] run
 * on the JVM in under a second, which is what makes it a test people actually run.
 */
object EnforcementEngine {

    /**
     * Packages that can never be suspended or hidden, whatever the policy says (FR-5.5).
     *
     * This list is the Kotlin copy of `policy.DefaultCriticalPackages`. It is held to the Go one by
     * the shared vector that blocks the dialer and settings explicitly and still expects them
     * absent from the suspension set — so a package dropped from one list and not the other is a
     * red test, not a phone that cannot dial.
     */
    val DEFAULT_CRITICAL_PACKAGES = listOf(
        "io.github.helios57.familyguard", // this DPC — suspending it would be unrecoverable
        "com.android.cellbroadcastreceiver", // emergency alerts
        "com.android.contacts",
        "com.android.dialer",
        "com.android.emergency", // emergency information
        "com.android.mms",
        "com.android.packageinstaller",
        "com.android.phone",
        "com.android.providers.contacts",
        "com.android.providers.telephony",
        "com.android.server.telecom",
        "com.android.settings",
        "com.google.android.apps.messaging",
        "com.google.android.contacts",
        "com.google.android.dialer",
        "com.google.android.packageinstaller",
        "com.google.android.permissioncontroller",
        "com.samsung.android.app.contacts",
        "com.samsung.android.dialer",
        "com.samsung.android.messaging",
    )

    /** The app family the YouTube killswitch suspends and hides (FR-7.1). */
    val YOUTUBE_PACKAGES = listOf(
        "app.revanced.android.youtube",
        "com.google.android.apps.youtube.kids",
        "com.google.android.apps.youtube.music",
        "com.google.android.youtube",
        "com.vanced.android.youtube",
        "org.schabi.newpipe",
    )

    /**
     * Blocked at the DNS layer and in the managed browser when the killswitch is on (FR-7.2,
     * FR-7.3). googlevideo.com carries the media itself; blocking only youtube.com leaves playback
     * working through an embed.
     */
    val YOUTUBE_DOMAINS = listOf(
        "googlevideo.com",
        "youtu.be",
        "youtube-nocookie.com",
        "youtube.com",
        "youtubei.googleapis.com",
        "youtubekids.com",
        "yt3.ggpht.com",
        "ytimg.com",
    )

    /**
     * Platform user-restriction keys, spelled out rather than referenced, because this file is a
     * pure Kotlin port with no Android import — that is what lets its tests run on the JVM in
     * milliseconds and what lets `vectors.json` compare it against the Go engine.
     *
     * The cost of copying them is that a wrong one is **silent**: `addUserRestriction` accepts an
     * unknown key, logs, and applies nothing, so the device reports itself hardened and enforces
     * nothing. Measured — [RESTRICTION_PRIVATE_DNS] was `no_config_private_dns` here and on the
     * server, which is what the `no_` convention every other key follows would suggest, and the
     * emulator silently dropped it. `DISALLOW_CONFIG_PRIVATE_DNS` is the exception in this set.
     *
     * `RestrictionKeysMatchThePlatformTest` is what makes copying them safe: it asserts each one
     * equals the `android.os.UserManager` constant it stands for.
     */
    const val RESTRICTION_SAFE_BOOT = "no_safe_boot"
    const val RESTRICTION_DEBUGGING = "no_debugging_features"
    const val RESTRICTION_PRIVATE_DNS = "disallow_config_private_dns"
    const val RESTRICTION_DATE_TIME = "no_config_date_time"
    const val RESTRICTION_ADD_USER = "no_add_user"
    const val RESTRICTION_UNKNOWN_SOURCES = "no_install_unknown_sources"
    const val RESTRICTION_INSTALL_APPS = "no_install_apps"
    const val RESTRICTION_UNINSTALL_APPS = "no_uninstall_apps"

    /**
     * The floor the DPC applies **before it has ever spoken to the server** — at provisioning
     * compliance, and on a boot that precedes the first successful sync.
     *
     * It is derived from the same list the engine builds from, so the two cannot disagree: a
     * separately written "provisioning defaults" list is a second policy nobody remembers to update.
     *
     * `no_debugging_features` is deliberately **not** here, and that was measured rather than
     * reasoned. Applying it as device owner switches `adb` off — the emulator went from `device` to
     * `offline` mid-test, taking the instrumented run with it, and no reboot brings it back because
     * the restriction outlives the boot. On a real phone the same call is irreversible from outside:
     * with the control plane unreachable and `adb` gone, the only way back in is a factory reset.
     * Two escape hatches exist for a device that has been provisioned and never synced — recovery
     * wipe and `adb` — and the pre-sync floor is not the place to spend one of them. It is a
     * legitimate policy restriction, so [compute] still emits it for a device that HAS synced (see
     * below), where the parent can see it and the server can withdraw it.
     */
    val BASELINE_RESTRICTIONS = listOf(
        RESTRICTION_SAFE_BOOT,
        RESTRICTION_PRIVATE_DNS,
        RESTRICTION_DATE_TIME,
        RESTRICTION_ADD_USER,
        RESTRICTION_UNKNOWN_SOURCES,
        RESTRICTION_UNINSTALL_APPS,
    )

    /** Never set. Named so that a test can assert their absence rather than trust a comment. */
    const val RESTRICTION_FACTORY_RESET = "no_factory_reset"
    const val RESTRICTION_OUTGOING_CALLS = "no_outgoing_calls"
    const val RESTRICTION_CREATE_WINDOWS = "no_create_windows"
    const val RESTRICTION_SMS = "no_sms"

    /**
     * Restrictions that can never reach DevicePolicyManager, filtered after the set is built.
     *
     * `no_factory_reset` is the one that matters most and the one most likely to be added back by
     * someone who thinks it sounds strict. A fully managed device that forbids factory reset can be
     * recovered from a bad policy, a wrong DNS host or a control plane that will not answer *only*
     * through the control plane — and if the control plane is what broke, it cannot be recovered at
     * all. Wiping from the recovery menu is the last escape hatch that depends on nothing this
     * project ships, and while the project is young that hatch stays open.
     *
     * None of the four is ever added below, so this filter removes nothing today: it is a net under
     * a future edit, not a live rule. Deleting it therefore changes no output, which is exactly why
     * the test for it simulates the mistake instead of removing the net.
     */
    val FORBIDDEN_RESTRICTIONS = setOf(
        RESTRICTION_FACTORY_RESET,
        RESTRICTION_OUTGOING_CALLS,
        RESTRICTION_CREATE_WINDOWS,
        RESTRICTION_SMS,
    )

    const val REASON_NONE = ""
    const val REASON_BEDTIME = "BEDTIME"
    const val REASON_QUOTA = "QUOTA"

    /**
     * RFC 3339 with seconds always present — the format Go's `time.RFC3339` produces, and the one
     * the shared vectors are written in.
     *
     * Spelling the pattern out is deliberate, but not for the reason it first appears: measured on
     * JDK 21, `ISO_OFFSET_DATE_TIME` formats a ZonedDateTime with `:00` seconds too, so swapping
     * this for it changes nothing today and no test goes red. It is `ZonedDateTime.toString()` that
     * drops the seconds field (and appends `[Europe/Zurich]`), which is the slip actually worth
     * defending against — that one is red, and the calibration record says so.
     */
    private val RFC3339: DateTimeFormatter = DateTimeFormatter.ofPattern("yyyy-MM-dd'T'HH:mm:ssXXX")

    /**
     * Evaluates the policy.
     *
     * @throws InvalidPolicyInput when the time zone, the instant or a bedtime clock cannot be read.
     * Refusing is deliberate: a fallback to UTC or to "bedtime off" would move or silently drop an
     * enforcement window, and nothing about the device would look wrong.
     */
    fun compute(input: Input): DesiredState {
        val zone = loadZone(input.settings.timezone)
        val local = parseInstant(input.now).atZoneSameInstant(zone)

        val critical = sortedSetOfPackages(DEFAULT_CRITICAL_PACKAGES) + input.criticalPackages.nonEmpty()

        // ---- filtering and hardening: in effect in every mode, including tracking-only ----

        val domains = sortedSetOf<String>()
        input.settings.blockedDomains.forEach { normalizeDomain(it)?.let(domains::add) }
        if (input.settings.youtubeBlocked) domains.addAll(YOUTUBE_DOMAINS)

        val restrictions = sortedSetOf<String>()
        restrictions.addAll(BASELINE_RESTRICTIONS)
        // Added here rather than in the baseline: a phone that has reached the server is one the
        // parent can un-restrict through it, so the escape hatch this closes is one the control
        // plane can reopen. Before that first sync there is nobody to ask. See BASELINE_RESTRICTIONS.
        restrictions.add(RESTRICTION_DEBUGGING)
        if (!input.settings.allowChildInstalls) restrictions.add(RESTRICTION_INSTALL_APPS)
        restrictions.removeAll(FORBIDDEN_RESTRICTIONS)

        val quota = input.settings.dailyLimitMinutes
        val base = DesiredState(
            // Only a parent's LOCK_NOW locks the keyguard. Bedtime and quota suspend apps instead:
            // a locked keyguard makes the phone less able to place an emergency call than a phone
            // whose apps are merely suspended.
            locked = input.parentLock,
            privateDnsHost = input.settings.dnsHost.trim(),
            blockedDomains = domains.toList(),
            safeSearch = true,
            youtubeRestrictedMode = true,
            allowInstalls = input.settings.allowChildInstalls,
            managedApps = normalizeManagedApps(input.settings.managedApps),
            userRestrictions = restrictions.toList(),
            quotaMinutes = quota,
            usedMinutes = input.usedMinutesToday,
            remainingMinutes = if (quota > 0) maxOf(0, quota - input.usedMinutesToday) else 0,
            policyVersion = input.settings.version,
        )

        // ---- tracking-only: measure, do not restrain (FR-8) ----
        //
        // The requirement is exact about which half is disabled: no quota, bedtime or app
        // suspension is enforced, while content filtering and hardening remain. So the YouTube
        // killswitch still blocks the domains and the browser but does not suspend the app —
        // suspension is suspension whatever caused it.
        if (input.settings.trackingOnly) return base

        // ---- enforcement ----

        // Every value here comes from the policy the server sent (FR-4.3). There is no default
        // window, no fallback hour and no constant anywhere in this file that a clock is compared
        // against: a schedule baked into the device is one the parent cannot change and cannot
        // see, and the first evidence of it would be a phone locking at an hour nobody chose.
        val inBedtime = if (input.settings.bedtimeEnabled) {
            val start = parseClock(input.settings.bedtimeStart, "bedtime_start")
            val end = parseClock(input.settings.bedtimeEnd, "bedtime_end")
            // start == end is disabled, not a 24-hour window. A window that can never be left is a
            // permanent lockdown reachable by one typo in the console.
            start != end && withinWindow(local.hour * 60 + local.minute, start, end)
        } else {
            false
        }
        val quotaReached = quota > 0 && input.usedMinutesToday >= quota

        val reason = when {
            inBedtime -> REASON_BEDTIME
            quotaReached -> REASON_QUOTA
            else -> REASON_NONE
        }

        val blocked = sortedSetOfPackages(input.settings.blockedPackages)
        if (input.settings.youtubeBlocked) blocked.addAll(YOUTUBE_PACKAGES)
        val allowed = sortedSetOfPackages(input.settings.allowedPackages)

        val suspended = sortedSetOf<String>()
        val hidden = sortedSetOf<String>()
        val pending = sortedSetOf<String>()

        // A blocked app is suspended and hidden (FR-5.2) whether or not it is installed right now:
        // the DPC applies the list, so an app installed later is already covered.
        suspended.addAll(blocked)
        hidden.addAll(blocked)

        for (app in input.installed) {
            if (app.pkg.isEmpty()) continue
            // FR-5.4: with free installation off, an app the child added after the device reported
            // its first inventory waits for a parent's decision.
            if (!input.settings.allowChildInstalls && app.newSinceBaseline && !app.system &&
                app.pkg !in allowed && app.pkg !in blocked
            ) {
                suspended.add(app.pkg)
                pending.add(app.pkg)
            }
            // Bedtime or an exhausted quota suspends everything non-exempt (FR-3.4, FR-4.2). An
            // explicit ALLOW rule is the exemption a parent can grant.
            if (reason != REASON_NONE && app.pkg !in allowed) suspended.add(app.pkg)
        }

        // The whitelist is applied last and unconditionally, so no branch above can outlive it.
        suspended.removeAll(critical)
        hidden.removeAll(critical)
        pending.removeAll(critical)

        return base.copy(
            suspendReason = reason,
            suspendedPackages = suspended.toList(),
            hiddenPackages = hidden.toList(),
            pendingApproval = pending.toList(),
            nextChangeAt = nextChangeAt(input.settings, local, zone),
        )
    }

    /**
     * The earliest future instant at which [compute] would return something different: the bedtime
     * edge, or local midnight when a quota is set and the counter resets.
     *
     * The device sets one exact alarm for this instead of polling, which is what keeps the phone
     * idle while the screen is off (NFR-10). An engine that returned "" here would look correct all
     * day and stop enforcing bedtime at the moment it mattered.
     */
    private fun nextChangeAt(settings: Settings, local: ZonedDateTime, zone: ZoneId): String {
        val candidates = mutableListOf<ZonedDateTime>()
        if (settings.bedtimeEnabled) {
            val start = runCatching { parseClock(settings.bedtimeStart, "bedtime_start") }.getOrNull()
            val end = runCatching { parseClock(settings.bedtimeEnd, "bedtime_end") }.getOrNull()
            if (start != null && end != null && start != end) {
                candidates += if (withinWindow(local.hour * 60 + local.minute, start, end)) {
                    nextOccurrence(local, end, zone)
                } else {
                    nextOccurrence(local, start, zone)
                }
            }
        }
        if (settings.dailyLimitMinutes > 0) {
            candidates += local.toLocalDate().plusDays(1).atStartOfDay(zone)
        }
        val next = candidates.minOrNull() ?: return ""
        return next.format(RFC3339)
    }

    /**
     * The next local wall-clock occurrence of [minute], strictly after [local].
     *
     * Built from a date and a time rather than by adding a duration, so that a daylight-saving
     * transition inside the interval does not shift the answer by an hour — which would be a
     * bedtime that ends at 06:00 or 08:00 twice a year.
     */
    private fun nextOccurrence(local: ZonedDateTime, minute: Int, zone: ZoneId): ZonedDateTime {
        val today = local.toLocalDate().atTime(minute / 60, minute % 60).atZone(zone)
        return if (today.isAfter(local)) today
        else local.toLocalDate().plusDays(1).atTime(minute / 60, minute % 60).atZone(zone)
    }

    /** True when [m] falls in [start, end), handling a window that crosses midnight. */
    private fun withinWindow(m: Int, start: Int, end: Int): Boolean =
        if (start < end) m >= start && m < end else m >= start || m < end

    private fun parseClock(value: String, field: String): Int {
        val parts = value.trim().split(":")
        if (parts.size != 2 || parts[0].length != 2 || parts[1].length != 2) {
            throw InvalidPolicyInput("$field: \"$value\" is not HH:MM")
        }
        val h = parts[0].toIntOrNull() ?: throw InvalidPolicyInput("$field: \"$value\" is not HH:MM")
        val m = parts[1].toIntOrNull() ?: throw InvalidPolicyInput("$field: \"$value\" is not HH:MM")
        if (h !in 0..23 || m !in 0..59) throw InvalidPolicyInput("$field: \"$value\" is out of range")
        return h * 60 + m
    }

    private fun loadZone(name: String): ZoneId {
        val n = name.trim()
        if (n.isEmpty()) throw InvalidPolicyInput("timezone must not be empty")
        return try {
            ZoneId.of(n)
        } catch (e: ZoneRulesException) {
            throw InvalidPolicyInput("unknown timezone \"$name\": ${e.message}")
        } catch (e: java.time.DateTimeException) {
            throw InvalidPolicyInput("unknown timezone \"$name\": ${e.message}")
        }
    }

    private fun parseInstant(now: String): OffsetDateTime = try {
        OffsetDateTime.parse(now)
    } catch (e: java.time.format.DateTimeParseException) {
        throw InvalidPolicyInput("now \"$now\" is not RFC 3339: ${e.message}")
    }

    /**
     * Matches the server's domain normalisation. It is duplicated rather than shared for the same
     * reason the engine is: the phone has to do this offline. Returns null for a value that
     * normalises to nothing, which is dropped rather than emitted as an empty entry.
     */
    private fun normalizeDomain(raw: String): String? {
        var d = raw.trim().lowercase()
        d = d.removePrefix("http://").removePrefix("https://")
        val cut = d.indexOfFirst { it == '/' || it == '?' || it == '#' }
        if (cut >= 0) d = d.substring(0, cut)
        d = d.trimEnd('.')
        return d.ifEmpty { null }
    }

    private fun sortedSetOfPackages(items: Iterable<String>) =
        sortedSetOf<String>().apply { addAll(items.nonEmpty()) }

    private fun Iterable<String>.nonEmpty() = filter { it.isNotEmpty() }

    /**
     * Sorts the declared set and drops what this device could not act on.
     *
     * Sorted and never null for the same reason every other list here is: the Go engine has to
     * produce byte-identical JSON for the shared vectors, and "null vs empty" is a difference that
     * shows up only in that comparison and never in a test that reads the field.
     *
     * An entry missing its package name, its URL or its checksum is DROPPED rather than passed on.
     * The phone cannot install it — there is nothing to fetch, or nothing to verify against — so
     * carrying it would produce a device reporting the same failure every sync, forever, about a
     * row nobody can see is malformed. Dropping it makes the app simply absent, which is what the
     * console already renders as "not available".
     *
     * Duplicates by package name collapse to the highest version code. Two rows for one package is
     * not reachable through the API, but this engine is also fed by the shared vectors, and
     * "install both versions of one package" is not a thing a phone can do.
     */
    private fun normalizeManagedApps(apps: List<ManagedApp>): List<ManagedApp> {
        val best = LinkedHashMap<String, ManagedApp>()
        for (raw in apps) {
            val app = raw.copy(
                packageName = raw.packageName.trim(),
                url = raw.url.trim(),
                checksum = raw.checksum.trim(),
            )
            if (app.packageName.isEmpty() || app.url.isEmpty() || app.checksum.isEmpty()) continue
            val existing = best[app.packageName]
            if (existing != null && existing.versionCode >= app.versionCode) continue
            best[app.packageName] = app
        }
        return best.values.sortedBy { it.packageName }
    }
}

/** Thrown for input the engine refuses to guess at. */
class InvalidPolicyInput(message: String) : IllegalArgumentException(message)

@Serializable
data class App(
    @SerialName("package") val pkg: String = "",
    @SerialName("system") val system: Boolean = false,
    @SerialName("new_since_baseline") val newSinceBaseline: Boolean = false,
)

@Serializable
data class Settings(
    @SerialName("tracking_only") val trackingOnly: Boolean = false,
    @SerialName("allow_child_installs") val allowChildInstalls: Boolean = false,
    @SerialName("youtube_blocked") val youtubeBlocked: Boolean = false,
    @SerialName("daily_limit_minutes") val dailyLimitMinutes: Int = 0,
    @SerialName("bedtime_enabled") val bedtimeEnabled: Boolean = false,
    @SerialName("bedtime_start") val bedtimeStart: String = "",
    @SerialName("bedtime_end") val bedtimeEnd: String = "",
    @SerialName("dns_host") val dnsHost: String = "",
    @SerialName("timezone") val timezone: String = "",
    @SerialName("version") val version: Long = 0,
    @SerialName("blocked_packages") val blockedPackages: List<String> = emptyList(),
    @SerialName("allowed_packages") val allowedPackages: List<String> = emptyList(),
    @SerialName("blocked_domains") val blockedDomains: List<String> = emptyList(),
    /**
     * The applications a parent has declared this child's phone should have (FR-16).
     *
     * A declared SET, not a queue of install commands: the device converges on it at every sync, so
     * an install that failed retries by itself and an app a child managed to remove comes back —
     * without a parent having to notice anything went wrong.
     */
    @SerialName("managed_apps") val managedApps: List<ManagedApp> = emptyList(),
)

/**
 * One entry of that set: which application, which exact build, and everything the phone needs to
 * fetch and verify it without asking a second question.
 *
 * The version is pinned rather than left as "latest". The phone compares [checksum] against the
 * bytes it downloads, so a URL that resolved to a newer build between the sync and the download
 * would fail that comparison — and the failure would read as a corrupted download rather than as a
 * race.
 *
 * [checksum] is base64url without padding, of the SHA-256 of the whole file: the same encoding
 * `/device/apk-info` publishes for the DPC, so this phone has one checksum format and not two.
 */
@Serializable
data class ManagedApp(
    @SerialName("package_name") val packageName: String = "",
    @SerialName("version_code") val versionCode: Long = 0,
    @SerialName("version_name") val versionName: String = "",
    @SerialName("checksum") val checksum: String = "",
    @SerialName("size") val size: Long = 0,
    @SerialName("url") val url: String = "",
)

@Serializable
data class Input(
    @SerialName("settings") val settings: Settings = Settings(),
    @SerialName("installed") val installed: List<App> = emptyList(),
    @SerialName("used_minutes_today") val usedMinutesToday: Int = 0,
    @SerialName("parent_lock") val parentLock: Boolean = false,
    @SerialName("critical_packages") val criticalPackages: List<String> = emptyList(),
    @SerialName("now") val now: String = "",
)

/**
 * What the device must make true. Every list is sorted and never null, so this engine and the Go one
 * serialise to byte-identical JSON and the shared vectors can be compared directly.
 */
@Serializable
data class DesiredState(
    @SerialName("locked") val locked: Boolean = false,
    @SerialName("suspend_reason") val suspendReason: String = "",
    @SerialName("suspended_packages") val suspendedPackages: List<String> = emptyList(),
    @SerialName("hidden_packages") val hiddenPackages: List<String> = emptyList(),
    @SerialName("pending_approval") val pendingApproval: List<String> = emptyList(),
    @SerialName("private_dns_host") val privateDnsHost: String = "",
    @SerialName("blocked_domains") val blockedDomains: List<String> = emptyList(),
    @SerialName("safe_search") val safeSearch: Boolean = false,
    @SerialName("youtube_restricted_mode") val youtubeRestrictedMode: Boolean = false,
    @SerialName("allow_installs") val allowInstalls: Boolean = false,
    @SerialName("user_restrictions") val userRestrictions: List<String> = emptyList(),
    @SerialName("quota_minutes") val quotaMinutes: Int = 0,
    @SerialName("used_minutes") val usedMinutes: Int = 0,
    @SerialName("remaining_minutes") val remainingMinutes: Int = 0,
    @SerialName("next_change_at") val nextChangeAt: String = "",
    @SerialName("policy_version") val policyVersion: Long = 0,
    /**
     * Passed through from the settings, sorted by package name and never null.
     *
     * The engine decides nothing about it — which applications a child has is a parent's
     * declaration, not a computed consequence of bedtime — but it travels in the desired state
     * rather than beside it, so the device has exactly one description of what it must make true
     * and the shared vectors cover it.
     *
     * A managed app is exempt from nothing. It is suspended at bedtime, hidden by a block rule and
     * counted against the quota like any other app: installing an application and governing it are
     * separate decisions, and a parent who declared one has not thereby allowed it at midnight.
     */
    @SerialName("managed_apps") val managedApps: List<ManagedApp> = emptyList(),
)
