package io.github.helios57.familyguard.recovery

import android.content.Context
import io.github.helios57.familyguard.enroll.EncryptedCredentialStore
import io.github.helios57.familyguard.net.ApiClient
import io.github.helios57.familyguard.net.RecoveryEventRequest
import io.github.helios57.familyguard.policy.DeviceOwnerPolicy
import io.github.helios57.familyguard.sync.deviceApplier
import java.time.Instant
import java.time.OffsetDateTime
import java.time.ZoneId
import java.time.format.DateTimeFormatter

/**
 * A [RecoveryController] wired to this device (FR-12).
 *
 * Built here rather than in the activity so that the screen holds no policy of its own: everything
 * this decides — which credential the material comes from, which stores persist the lockout, which
 * appliers release the phone — is the same wiring `ConnectionService` uses, and a screen that built
 * its own would be a second answer to "what does releasing mean".
 *
 * **Every call in here touches disk or the keystore.** `EncryptedSharedPreferences` opens a keystore
 * key, and verifying a code is 120 000 rounds of PBKDF2. Call this off the main thread; the activity
 * does.
 *
 * The journal's `send` is real rather than a stub that throws, even though the screen only ever
 * *records*: the same controller is the one thing that could deliver an event on a device whose
 * service is not running, and a lambda documented as unreachable is a lambda that will one day be
 * reached.
 */
fun androidRecoveryController(context: Context): RecoveryController {
    val app = context.applicationContext
    val credentials = EncryptedCredentialStore(app)
    val stores = AndroidRecoveryStore(app)
    return RecoveryController(
        material = { credentials.load()?.recovery },
        lockout = RecoveryLockout(stores.lockout),
        mode = RecoveryMode(stores.mode),
        journal = RecoveryJournal(stores.journal) { attempt ->
            val current = credentials.load()
                ?: throw IllegalStateException("this device has no credential to report with")
            ApiClient(current.serverUrl, token = { credentials.load()?.deviceToken })
                .reportRecoveryEvent(
                    RecoveryEventRequest(
                        succeeded = attempt.succeeded,
                        occurredAt = OffsetDateTime.ofInstant(
                            Instant.ofEpochMilli(attempt.occurredAtEpochMillis),
                            ZoneId.systemDefault(),
                        ).format(RFC3339),
                    )
                )
        },
        // The full applier stack, not a shortcut. Releasing means clearing user restrictions, app
        // suspensions, Chrome policy, private DNS and the parent lock — and the one that gets
        // forgotten by a hand-written shortcut is whichever one the phone was stuck on.
        release = {
            deviceApplier(
                DeviceOwnerPolicy.of(app),
                credentials.load()?.serverUrl.orEmpty(),
            ).apply(releasedState())
        },
    )
}

/** See `ConnectionService.RFC3339`: spelled out because `toString()` drops a zero seconds field. */
private val RFC3339: DateTimeFormatter = DateTimeFormatter.ofPattern("yyyy-MM-dd'T'HH:mm:ssXXX")
