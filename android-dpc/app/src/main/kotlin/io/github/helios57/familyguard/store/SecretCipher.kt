package io.github.helios57.familyguard.store

import java.security.GeneralSecurityException

/**
 * Sealing and opening one short value, with a caller-supplied context string bound into the result.
 *
 * Split out from [CipherPreferences] for one reason: the preferences façade — the typed encoding,
 * the context strings, the behaviour when a stored value will not open — is ordinary Kotlin that a
 * JVM unit test can drive, and the keystore is not. Everything above this interface is tested on the
 * host, [KeystoreSecretCipher] is tested on a device, and neither test can pass by not running.
 *
 * [context] is *authenticated but not stored*: it goes in as additional authenticated data, so a
 * sealed value only opens when the caller names the same context it was sealed under. The
 * preferences façade names the file and the preference key, which is what makes a stolen blob
 * un-moveable — a credential lifted out of one file cannot be dropped into another, and the lockout
 * counter cannot be replaced with the value from a different key that happens to open cleanly.
 *
 * It does **not** stop a rollback of the same key in the same file: an attacker holding a copy of
 * yesterday's file can put yesterday's value back, and this layer cannot tell that from a value that
 * was never changed. Defeating that needs a counter the device keeps somewhere the attacker cannot
 * roll back with it, which is a different mechanism and is deliberately not claimed here.
 */
interface SecretCipher {

    /** Seals [plaintext] under [context]. The result is safe to store as a preference string. */
    @Throws(GeneralSecurityException::class)
    fun seal(context: String, plaintext: ByteArray): ByteArray

    /**
     * Opens what [seal] produced under the *same* [context].
     *
     * Throws for every reason a value can fail to open — a wrong context, a truncated or edited
     * blob, a key that is gone — and deliberately does not distinguish them. The caller's answer is
     * the same in every case, and a decryption oracle that reports *why* it failed is a decryption
     * oracle.
     */
    @Throws(GeneralSecurityException::class)
    fun open(context: String, sealed: ByteArray): ByteArray
}
