package io.github.helios57.familyguard.status

import android.content.Context
import io.github.helios57.familyguard.enroll.EncryptedCredentialStore
import io.github.helios57.familyguard.policy.DeviceOwnerPolicy
import io.github.helios57.familyguard.recovery.AndroidRecoveryStore
import io.github.helios57.familyguard.recovery.RecoveryJournal
import io.github.helios57.familyguard.recovery.RecoveryMode
import io.github.helios57.familyguard.sync.EncryptedPolicyCache
import io.github.helios57.familyguard.usage.DayAttribution
import io.github.helios57.familyguard.usage.EncryptedUsageStore
import io.github.helios57.familyguard.usage.ForegroundReader
import io.github.helios57.familyguard.usage.UsageLedger
import io.github.helios57.familyguard.usage.UsageStatsForegroundReader
import java.net.URI
import java.time.ZoneId

/**
 * Reads this device's own state into [StatusFacts], so [deviceStatus] can turn it into a screen.
 *
 * **Every call in here touches disk, the keystore or a system service.** Three encrypted preference
 * files are opened and the usage stats manager is queried; on a cheap handset that is comfortably
 * long enough to be an ANR. Call it off the main thread — `RecoveryActivity` does.
 *
 * The split is the point. Nothing in this file decides what a fact *means*: it produces nulls and
 * numbers, and every judgement — what counts as stale, what is an attention, what must never render
 * as zero — is in the pure half, where it is asserted on the JVM in milliseconds. A gatherer that
 * also formatted would put those decisions somewhere only a handset can check.
 *
 * Nothing here can throw past the caller. A status screen is what a parent reaches for when the
 * phone is already misbehaving, so a store that fails to open has to become a line that says so,
 * never a crash on the one screen that was going to explain the problem. That is why each read is
 * wrapped: the failure of one store costs its own line, not the screen.
 */
fun deviceStatusFacts(
    context: Context,
    nowMillis: Long = System.currentTimeMillis(),
): StatusFacts {
    val app = context.applicationContext

    val credentials = runCatching { EncryptedCredentialStore(app).load() }.getOrNull()
    val cache = runCatching { EncryptedPolicyCache(app) }.getOrNull()
    val cached = cache?.let { runCatching { it.load() }.getOrNull() }
    val stores = runCatching { AndroidRecoveryStore(app) }.getOrNull()

    // `DeviceOwnerPolicy.of` returns null for "not the device owner" — a real, measured answer. A
    // *throw* is the different thing: the device-policy service was not reachable at all, which is
    // not evidence either way and must not be reported as "not managed".
    val deviceOwner = runCatching { DeviceOwnerPolicy.of(app) != null }.getOrNull()

    // The zone the ledger attributed usage in, so "today" here is the same today the quota is
    // measured against. A policy zone that will not parse falls back to the device's own — which is
    // what the tracker does with it too, so the two agree even when both are wrong.
    val zone = cached?.settings?.timezone
        ?.let { DayAttribution.zoneOf(it) }
        ?: ZoneId.systemDefault()
    val today = DayAttribution.key(nowMillis, zone)

    val reader = UsageStatsForegroundReader(app)
    val screenTime = screenTimeToday(app, reader, today, nowMillis)

    return StatusFacts(
        deviceId = credentials?.deviceId,
        serverHost = credentials?.serverUrl?.let(::hostOf),
        deviceOwner = deviceOwner,
        releasedSinceMillis = stores?.let { runCatching { RecoveryMode(it.mode).activeSince() }.getOrNull() },
        appliedPolicyVersion = cache?.let { runCatching { it.appliedVersion() }.getOrNull() } ?: 0L,
        cachedPolicyVersion = cached?.settings?.version,
        lastServerContactMillis = cache?.let { runCatching { it.lastServerContact() }.getOrNull() } ?: 0L,
        screenTimeTodayMillis = screenTime,
        screenTimeUnavailableReason = runCatching { reader.unavailableReason() }
            .getOrDefault(UNKNOWN_USAGE_REASON),
        quotaMinutes = cached?.settings?.dailyLimitMinutes ?: 0,
        unreportedRecoveryAttempts = stores
            ?.let { runCatching { RecoveryJournal(it.journal) { }.outstanding().size }.getOrNull() }
            ?: 0,
        nowMillis = nowMillis,
    )
}

/**
 * Today's total, or **null when this phone cannot measure usage at all**.
 *
 * The reader is asked first, and its answer decides. Reading the ledger alone cannot distinguish the
 * two cases that matter: a phone with usage access that has been idle since midnight has a total of
 * zero, and so does a phone whose usage access was revoked at breakfast — and on the second one the
 * daily limit will never be reached, all day, with nothing anywhere saying so.
 *
 * The probe window is deliberately short. `spans` returns null for a device that cannot see usage,
 * whatever the window, so a minute is enough to ask the question and cheap enough to ask it on a
 * screen somebody is waiting for.
 */
private fun screenTimeToday(
    context: Context,
    reader: ForegroundReader,
    today: String,
    nowMillis: Long,
): Long? {
    // An empty list here is a phone that could see usage and found none in the last minute — the
    // common case on a screen somebody is reading, and not an absence. Only null is the absence,
    // and a throw is treated as one too: a reader that blew up measured nothing either.
    val probe = runCatching { reader.spans(nowMillis - PROBE_WINDOW_MILLIS, nowMillis) }.getOrNull()
    if (probe == null) return null
    return runCatching { UsageLedger(EncryptedUsageStore(context)).totals(today).values.sum() }
        .getOrNull()
}

/**
 * The host of the server URL, for a parent to check against the console they are logged into.
 *
 * The host and not the URL: a full URL invites somebody to type it into a browser, where it answers
 * nothing useful, and the port and path are noise on a screen read at arm's length. A URL that will
 * not parse yields null, which renders as an enrollment line without a host rather than as a line
 * showing whatever unparseable string was stored.
 */
private fun hostOf(url: String): String? =
    runCatching { URI(url).host }.getOrNull()?.takeIf { it.isNotBlank() }

private const val PROBE_WINDOW_MILLIS = 60_000L

/**
 * What the screen says when even the *reason* could not be read.
 *
 * Its own string rather than an empty one: an empty value renders as a blank line next to a
 * NOT_MEASURED label, which reads like a rendering bug and sends a parent looking at the wrong
 * thing.
 */
private const val UNKNOWN_USAGE_REASON =
    "not measured — this phone could not say why it cannot see screen time"
