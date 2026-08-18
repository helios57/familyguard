package io.github.helios57.familyguard.enroll

import io.github.helios57.familyguard.net.RecoveryMaterial
import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable

/**
 * Everything this device learned by enrolling, and the only thing that distinguishes it from a
 * freshly installed APK.
 *
 * [recovery] is verification material, not a code: salt, iteration count and hash. The plaintext
 * recovery code exists once, on the parent's screen, at enrollment. A device that stored it could
 * display it — and a device that can display the code that unlocks it is not locked.
 */
@Serializable
data class Credentials(
    @SerialName("server_url") val serverUrl: String,
    @SerialName("device_token") val deviceToken: String,
    @SerialName("device_id") val deviceId: String,
    @SerialName("child_id") val childId: String,
    @SerialName("recovery") val recovery: RecoveryMaterial = RecoveryMaterial(),
)

/**
 * Where [Credentials] live between boots.
 *
 * An interface because the only implementation needs a real Android `Context` and a keystore, and
 * because everything worth testing about enrollment — is it idempotent, what happens when the token
 * has already been spent, what happens when the extras are wrong — is about *when* this is written,
 * not about how. See [InMemoryCredentialStore] in the tests.
 */
interface CredentialStore {
    /** The stored credentials, or null on a device that has never enrolled. */
    fun load(): Credentials?

    /** Replaces the stored credentials as one unit. */
    fun save(credentials: Credentials)

    /** Forgets them. Only a factory reset or an explicit un-enrollment should reach this. */
    fun clear()
}
