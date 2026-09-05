package io.github.helios57.familyguard.sync

import io.github.helios57.familyguard.enforce.DesiredState
import io.github.helios57.familyguard.policy.AppSuspensionManager
import io.github.helios57.familyguard.policy.ChromePolicyManager
import io.github.helios57.familyguard.policy.DeviceOwnerPolicy
import io.github.helios57.familyguard.policy.DnsPolicyManager
import io.github.helios57.familyguard.policy.HardeningManager
import io.github.helios57.familyguard.policy.LockManager
import java.net.URI
import java.net.URISyntaxException

/**
 * What one applier did, read back from the platform rather than assumed from the calls it made.
 *
 * [problems] is keyed by whatever the applier failed at — a restriction name, a package, "dns" —
 * so a partial failure names its half instead of collapsing to one boolean. An empty map is the
 * only thing that counts as success, and it is what lets the device claim a policy version.
 */
data class ApplyOutcome(val summary: String, val problems: Map<String, String> = emptyMap()) {
    val ok: Boolean get() = problems.isEmpty()

    override fun toString(): String =
        if (ok) summary else "$summary PROBLEMS=$problems"
}

/**
 * One half of making a [DesiredState] true on this device.
 *
 * Split into appliers rather than written as one method because they fail independently: an OEM
 * that rejects a user restriction must not cost the device its DNS host, and the parent needs to
 * be told which of the two it was. Each applier is responsible for reading back what it did.
 */
interface StateApplier {
    fun apply(state: DesiredState): ApplyOutcome
}

/**
 * Runs several appliers and merges what they report.
 *
 * Every applier runs, whatever the ones before it reported. Stopping at the first problem would
 * make a device that cannot set its DNS host also stop suspending apps at bedtime — one failure
 * turning into an unmanaged phone.
 *
 * Problem keys are prefixed with the applier's own name, because two appliers can legitimately fail
 * at the same key: "youtube.com" is both a blocked domain and a Chrome policy entry, and a merged
 * map without the prefix would silently keep one of the two.
 */
class CompositeApplier(private val appliers: List<Pair<String, StateApplier>>) : StateApplier {

    override fun apply(state: DesiredState): ApplyOutcome {
        val summaries = mutableListOf<String>()
        val problems = LinkedHashMap<String, String>()
        for ((name, applier) in appliers) {
            val outcome = try {
                applier.apply(state)
            } catch (e: RuntimeException) {
                // An applier that throws is a bug in that applier, not a reason for the device to
                // stop enforcing everything else. It is recorded as loudly as a returned problem.
                ApplyOutcome("$name threw", mapOf("!" to (e.message ?: e.javaClass.simpleName)))
            }
            summaries += "$name[${outcome.summary}]"
            outcome.problems.forEach { (key, value) -> problems["$name/$key"] = value }
        }
        return ApplyOutcome(summaries.joinToString(" "), problems)
    }
}

/**
 * Applies `user_restrictions`, through the same [HardeningManager] the provisioning and boot paths
 * use — but through its authoritative half.
 *
 * `apply` rather than `applyBaseline`: the server has just said what it wants, so a managed
 * restriction it did not ask for is cleared. That is the only path in this app allowed to weaken
 * the device, and it runs only in response to something the parent can see and change.
 */
class RestrictionApplier(private val hardening: HardeningManager) : StateApplier {

    override fun apply(state: DesiredState): ApplyOutcome {
        val outcome = hardening.apply(state.userRestrictions)
        val problems = LinkedHashMap<String, String>()
        outcome.failures.forEach { (key, value) -> problems[key] = value }
        // Requested, did not throw, and still not in effect. `addUserRestriction` is a request, and
        // an OEM that accepts one it does not implement produces exactly this: a device that
        // believes it is hardened and is not.
        //
        // A restriction that *threw* is missing too, and reporting it here as well would overwrite
        // the exception's message with the word "accepted" — the parent would be told the platform
        // agreed and quietly did nothing, when it had said no out loud. Same key, and the specific
        // fact is the one worth keeping.
        outcome.missing
            .filter { it !in outcome.failures }
            .forEach { problems[it] = "requested, accepted, and not in effect" }
        // FR-2.3 / NFR-6. Nothing this app does sets it, so reaching here means something else on
        // the device did — and the phone can no longer be wiped from recovery.
        outcome.stillForbidden.forEach { problems[it] = "forbidden restriction still in effect" }
        return ApplyOutcome(
            summary = "added=${outcome.added.size} cleared=${outcome.cleared.size}",
            problems = problems,
        )
    }
}

/**
 * Applies `suspended_packages` and `hidden_packages` (FR-5.1..FR-5.5).
 *
 * The whole desired set is passed every time, never a delta: the manager converges on it, which is
 * what makes a device that missed a sync — or was restrained by a policy the parent has since
 * deleted — repair itself on the next one rather than staying wrong until someone notices.
 */
class AppApplier(private val apps: AppSuspensionManager) : StateApplier {

    override fun apply(state: DesiredState): ApplyOutcome {
        val outcome = apps.apply(state.suspendedPackages, state.hiddenPackages)
        val problems = LinkedHashMap<String, String>()
        outcome.failures.forEach { (key, value) -> problems[key] = value }
        // `missing` is the accepted-and-not-in-effect half. It is written after `failures` and does
        // not overwrite it, for the reason RestrictionApplier gives: a package the platform refused
        // out loud is more informative than the same package described as accepted.
        outcome.missing.forEach { (key, value) -> problems.putIfAbsent(key, value) }
        outcome.stillRestrained.forEach {
            problems[it] = "critical package is suspended or hidden on this device"
        }
        return ApplyOutcome(summary = outcome.toString(), problems = problems)
    }
}

/**
 * Applies the browser half — blocklist, SafeSearch, YouTube restricted mode (FR-6.3, FR-7.3).
 *
 * @param neverBlocked hosts the blocklist may never cover (FR-6.5). In practice one: the control
 * plane. A parent who blocks `example.com` to keep a child off one site would otherwise cut the phone
 * off from the console that is the only way to undo it — a self-inflicted lockout with no path back
 * that does not involve a factory reset.
 */
class ChromeApplier(
    private val chrome: ChromePolicyManager,
    private val neverBlocked: List<String>,
) : StateApplier {

    override fun apply(state: DesiredState): ApplyOutcome {
        val outcome = chrome.apply(
            blockedDomains = state.blockedDomains,
            safeSearch = state.safeSearch,
            youtubeRestricted = state.youtubeRestrictedMode,
            neverBlocked = neverBlocked,
        )
        val problems = LinkedHashMap<String, String>()
        outcome.failure?.let { problems["!"] = it }
        outcome.missing.forEach { (key, value) -> problems[key] = value }
        return ApplyOutcome(summary = outcome.toString(), problems = problems)
    }

    companion object {
        /**
         * The host part of the control-plane URL, as a Chrome filter entry.
         *
         * Returns an empty list rather than guessing when the URL will not parse or carries no host.
         * An allowlist entry of `""` is not neutral — Chrome reads it as a pattern matching
         * everything, which would turn the one entry meant to protect the console into a switch that
         * disables the entire blocklist. Failing to protect the console is recoverable by a parent
         * with a second device; silently unblocking the internet is not visible at all.
         */
        fun allowlistFor(serverUrl: String): List<String> {
            val host = try {
                URI(serverUrl.trim()).host
            } catch (_: URISyntaxException) {
                null
            }
            return if (host.isNullOrBlank()) emptyList() else listOf(host)
        }
    }
}

/**
 * Applies `locked` — the standing parent lock a `LOCK_NOW` sets and an `UNLOCK_DEVICE` clears
 * (FR-9).
 *
 * It is an applier rather than only a command handler because the flag outlives the command. A phone
 * that was offline when the parent locked it, or that rebooted afterwards, has to come back locked —
 * and the only thing that survives both is the desired state, which is recomputed from the cached
 * policy on every sync and every boot.
 *
 * `locked = false` does nothing at all. That is not an omission: there is no platform call that
 * dismisses a keyguard, and [LockManager] says why. Clearing the flag stops the re-locking, which is
 * the whole of the device's part in `UNLOCK_DEVICE`.
 *
 * A device with no lock-screen credential reports a problem here every time, which holds the applied
 * policy version back for as long as the parent lock is set. That is deliberate and it is
 * actionable in two ways — set a credential on the child's phone, or clear the lock — and both are
 * better than a console that shows a phone as locked while the child is using it.
 */
class LockApplier(private val lock: LockManager) : StateApplier {

    override fun apply(state: DesiredState): ApplyOutcome {
        if (!state.locked) return ApplyOutcome("no parent lock")
        val outcome = lock.lock()
        val problems = LinkedHashMap<String, String>()
        outcome.failure?.let { problems["keyguard"] = it }
        return ApplyOutcome(
            summary = outcome.summary + (outcome.note?.let { " ($it)" } ?: ""),
            problems = problems,
        )
    }
}

/** Applies `private_dns_host` (FR-6.1, FR-6.2). */
class DnsApplier(private val dns: DnsPolicyManager) : StateApplier {

    override fun apply(state: DesiredState): ApplyOutcome {
        val outcome = dns.apply(state.privateDnsHost)
        val problems = LinkedHashMap<String, String>()
        outcome.failure?.let { problems["private_dns"] = it }
        return ApplyOutcome(summary = outcome.summary, problems = problems)
    }
}

/**
 * The appliers, in the order that decides what a half-applied phone is left enforcing.
 *
 * Managed apps first, when there are any: an application installed in this pass is then suspended
 * or hidden by the pass's own app work, rather than sitting usable on a child's phone until the
 * next sync. It also puts the two restrictions it lifts back before [RestrictionApplier] asserts
 * the authoritative set over them, so the lift is repaired within the same pass even if it failed.
 *
 * Restrictions next because they are what holds when everything after them has failed; DNS after
 * the app work because it is the only one that can be refused for a reason outside the device (a
 * host that is not answering), and a refusal there must not cost the other three. The lock is last
 * of all: it is the only applier the child can *see* happen, so the silent work finishes before the
 * screen goes dark.
 *
 * A top-level function rather than a private method of [ConnectionService], because the recovery
 * screen applies the released state too (FR-12.2) — and it must apply it through *this* stack. Two
 * wirings would be two answers to "what does this phone enforce", and the one that ran on the day
 * somebody was locked out is the one nobody tested.
 *
 * @param policy null on a device this app does not own, which is not an error here — see
 * [NoDeviceOwnerApplier] for why that case must report a problem rather than a clean apply.
 * @param managedApps the FR-16 pass, or null to leave the phone's applications alone. **The
 * recovery release passes null, and must.** Its released state carries no declared set, and an
 * empty declared set is indistinguishable from "the parent withdrew everything" — a parent
 * unlocking a phone in an emergency would have every application this system installed removed
 * from it. Releasing a lock is not a statement about which apps a child should have.
 */
fun deviceApplier(
    policy: DeviceOwnerPolicy?,
    serverUrl: String,
    managedApps: StateApplier? = null,
): StateApplier {
    if (policy == null) return NoDeviceOwnerApplier
    return CompositeApplier(
        buildList {
            managedApps?.let { add("managed" to it) }
            add("restrictions" to RestrictionApplier(policy.hardening))
            add("apps" to AppApplier(policy.apps))
            add("chrome" to ChromeApplier(policy.chrome, ChromeApplier.allowlistFor(serverUrl)))
            add("dns" to DnsApplier(policy.dns))
            add("lock" to LockApplier(policy.lock))
        }
    )
}

/**
 * The applier for a device this app does not own: it applies nothing and says so.
 *
 * Named rather than a lambda because what it returns is the load-bearing part. Reporting success
 * would make every sync look clean while the phone enforced nothing, and it would let the device
 * claim a policy version it never applied.
 */
object NoDeviceOwnerApplier : StateApplier {
    override fun apply(state: DesiredState): ApplyOutcome = ApplyOutcome(
        "nothing applied",
        mapOf("device_owner" to "this app is not the device owner"),
    )
}
