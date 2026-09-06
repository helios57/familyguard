package io.github.helios57.familyguard.enroll

import io.github.helios57.familyguard.net.ApiClient
import io.github.helios57.familyguard.net.ApiException
import io.github.helios57.familyguard.net.EnrollRequest
import java.io.IOException
import java.net.URI

/** What this hardware calls itself, for the console's device list and for the critical-app floor. */
data class DeviceFacts(
    val model: String = "",
    val osVersion: String = "",
    /**
     * This device's own dialer, launcher and IME. The server unions them with its built-in list and
     * never narrows it, so reporting nothing is no worse than the floor — but reporting an OEM
     * dialer is what stops bedtime from suspending the phone's ability to call for help.
     */
    val criticalPackages: List<String> = emptyList(),
)

/** The outcome of one enrollment attempt. Five outcomes, because four of them are not failures. */
sealed interface EnrollResult {
    /** A credential was obtained and stored. */
    data class Enrolled(val credentials: Credentials) : EnrollResult

    /** This device already had one. Nothing was sent. */
    data class AlreadyEnrolled(val credentials: Credentials) : EnrollResult

    /**
     * The provisioning extras cannot produce an enrollment — a missing server URL, a cleartext one.
     * Retrying cannot fix it: the QR code has to be regenerated and the device re-provisioned.
     */
    data class Misprovisioned(val reason: String) : EnrollResult

    /** The server refused, permanently: a spent, expired or unknown enrollment token. */
    data class Refused(val cause: ApiException) : EnrollResult

    /** Nothing is wrong that waiting cannot fix — no network yet, or a backend restarting. */
    data class Deferred(val cause: Exception) : EnrollResult
}

/**
 * Exchanges the single-use enrollment token from the provisioning QR for this device's credential.
 *
 * Deliberately Android-free: it takes the admin extras as a plain map, because everything worth
 * testing here is about *decisions* — is this idempotent, is a spent token distinguishable from a
 * network outage, is a cleartext server URL refused — and none of those need a `PersistableBundle`
 * to be true. The Android side's whole job is to read the bundle into a map.
 *
 * **Idempotent by design.** `onProfileProvisioningComplete` is not a promise of exactly-once
 * delivery, the setup activity can be re-entered, and the enrollment token is spent by the first
 * call. A second attempt that re-enrolled would answer 409 and could leave a working device
 * believing it had failed.
 */
class Enroller(
    private val store: CredentialStore,
    /**
     * Whether an `http://` server URL may be accepted, and even then only for loopback and the
     * emulator's host alias. False in a release build: a cleartext MDM channel is a policy that
     * anyone on the network can rewrite, and the phone would never know.
     */
    private val cleartextAllowed: Boolean = false,
    private val clientFor: (String) -> ApiClient = { ApiClient(it, token = { null }) },
) {

    fun enroll(extras: Map<String, String?>, facts: DeviceFacts = DeviceFacts()): EnrollResult {
        // Checked before the extras are even read. A device that has enrolled must never spend a
        // second token, and the stored credential is the authority on whether it has.
        store.load()?.let { return EnrollResult.AlreadyEnrolled(it) }

        val serverUrl = extras[EXTRA_SERVER_URL]?.trim().orEmpty()
        val token = extras[EXTRA_ENROLLMENT_TOKEN]?.trim().orEmpty()
        if (serverUrl.isEmpty()) {
            return EnrollResult.Misprovisioned("the provisioning extras carry no $EXTRA_SERVER_URL")
        }
        if (token.isEmpty()) {
            return EnrollResult.Misprovisioned("the provisioning extras carry no $EXTRA_ENROLLMENT_TOKEN")
        }
        return exchange(serverUrl, token, facts)
    }

    /**
     * Exchanges a fresh setup code for a new credential on a phone that is ALREADY enrolled and
     * whose credential the server no longer recognises (FR-1.8).
     *
     * This is the deliberate exception to the idempotence guard in [enroll], and it exists because
     * without it there is no way back at all. Issuing a new setup code from the console revokes the
     * device server-side — `device_token_hash` and `enrolled_at` are nulled — and the phone cannot
     * notice, because [enroll] answers `AlreadyEnrolled` for as long as anything is stored. The
     * phone then holds a credential nothing will accept, forever, and the only remedy was a factory
     * reset: on a phone that had been in a child's hands for weeks, over one tap of a button that
     * used to be labelled "Setup QR". That happened, on the first real phone this project enrolled.
     *
     * Three properties make this safe to expose on the one screen a person can start:
     *
     *  - **The server URL comes from the STORED credential, never from what was typed.** A setup
     *    code is a bearer token and nothing else; it cannot move this phone to a different control
     *    plane, so the worst a typed string can do is fail.
     *  - **It needs a code the server minted.** Re-linking is not something the device can decide
     *    to do — a parent has to issue a setup code in the console, which is the same authority
     *    that revoked it. A lost phone that a parent deliberately cut off stays cut off.
     *  - **It replaces the credential as one unit**, including the recovery material, because the
     *    server generates new material on every enrollment. The old recovery code stops working the
     *    moment this succeeds, and the console shows the new one.
     *
     * @param setupCode the enrollment token, as the console shows it.
     */
    fun relink(setupCode: String, facts: DeviceFacts = DeviceFacts()): EnrollResult {
        val current = store.load()
            ?: return EnrollResult.Misprovisioned(
                "this device has no credential to re-link: it has never enrolled"
            )
        val token = setupCode.trim()
        if (token.isEmpty()) return EnrollResult.Misprovisioned("no setup code was entered")
        return exchange(current.serverUrl, token, facts)
    }

    /**
     * The half [enroll] and [relink] share: validate the URL, spend the token, store what comes
     * back. Everything above it is about *whether* to do this; nothing below it knows which caller
     * asked.
     */
    private fun exchange(serverUrl: String, token: String, facts: DeviceFacts): EnrollResult {
        // The reason is a message a parent may end up reading off the phone, and it must never carry
        // the enrollment token: it is a bearer credential until it is spent.
        urlProblem(serverUrl)?.let { return EnrollResult.Misprovisioned(it) }

        val response = try {
            clientFor(serverUrl).enroll(
                EnrollRequest(
                    enrollmentToken = token,
                    model = facts.model,
                    osVersion = facts.osVersion,
                    criticalPackages = facts.criticalPackages,
                )
            )
        } catch (e: ApiException) {
            return if (e.retryable) EnrollResult.Deferred(e) else EnrollResult.Refused(e)
        } catch (e: IOException) {
            // A phone provisioned on a home network reaches this on the first attempt more often
            // than not: Wi-Fi is associated but DNS has not settled.
            return EnrollResult.Deferred(e)
        }

        if (response.deviceToken.isEmpty() || response.deviceId.isEmpty()) {
            // Storing this would make the device believe it had enrolled while being unable to
            // authenticate — the one outcome from which it cannot recover on its own, because the
            // enrollment token is now spent and the guard above would refuse to try again.
            return EnrollResult.Deferred(
                IOException("the server accepted the enrollment but returned no usable credential")
            )
        }

        val credentials = Credentials(
            serverUrl = serverUrl,
            deviceToken = response.deviceToken,
            deviceId = response.deviceId,
            childId = response.childId,
            recovery = response.recovery,
        )
        store.save(credentials)
        return EnrollResult.Enrolled(credentials)
    }

    /** Null when the URL may be used. Mirrors the rule the server applies when building the QR. */
    private fun urlProblem(raw: String): String? {
        val uri = try {
            URI(raw)
        } catch (_: Exception) {
            return "the server URL in the provisioning extras is not a URL"
        }
        val host = uri.host
        // The scheme check below already refuses everything that is not absolute — a URI with no
        // scheme cannot be `https` — so there is no separate absoluteness test here. This one is not
        // redundant with it: `https:///api` is absolute, is https, and names nobody.
        if (host.isNullOrEmpty()) {
            return "the server URL in the provisioning extras names no host"
        }
        if (uri.scheme == "https") return null
        if (uri.scheme == "http" && cleartextAllowed && host in CLEARTEXT_HOSTS) return null
        return "the server URL in the provisioning extras is not https"
    }

    companion object {
        // The keys the server writes into the admin extras bundle. Spelled here rather than derived,
        // because a mismatch is silent on both sides: the server writes an extra nobody reads and the
        // device finds an extra that is not there.
        const val EXTRA_SERVER_URL = "server_url"
        const val EXTRA_ENROLLMENT_TOKEN = "enrollment_token"
        const val EXTRA_DEVICE_ID = "device_id"

        /** Loopback and the emulator's alias for its host. Nothing else is ever cleartext. */
        private val CLEARTEXT_HOSTS = setOf("localhost", "127.0.0.1", "10.0.2.2")
    }
}
