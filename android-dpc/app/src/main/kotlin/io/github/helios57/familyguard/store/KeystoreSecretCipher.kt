package io.github.helios57.familyguard.store

import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import java.security.GeneralSecurityException
import java.security.InvalidKeyException
import java.security.KeyStore
import java.util.concurrent.ConcurrentHashMap
import javax.crypto.Cipher
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec

/**
 * [SecretCipher] over an AES-256-GCM key the Android Keystore holds and never hands back.
 *
 * This replaced `androidx.security:security-crypto`, which reached 1.1.0 stable with its entire
 * public surface — `MasterKey`, `EncryptedSharedPreferences` and every nested scheme — marked
 * deprecated. A wholesale-deprecated crypto library is not one to keep a bearer credential in: it
 * gets no further fixes and no compatibility guarantee against the next platform release, and the
 * replacement Google names is the keystore directly. Dropping it also drops a dependency, which is
 * one fewer versioned thing that has to be watched.
 *
 * **What the keystore gives and what it does not.** The key material lives in the TEE and cannot be
 * extracted by root; every seal and open is a call into it. That does not make a rooted phone safe —
 * root can ask this app's uid to decrypt anything it likes — it makes the *files* useless on their
 * own. A backup of `/data`, a pulled image, a copied `shared_prefs` directory: all of them come out
 * as ciphertext bound to a key that stayed on the device.
 *
 * **Deliberately not StrongBox.** `setIsStrongBoxBacked(true)` would put the key in the secure
 * element on devices that have one, but the secure element is slow enough that pushing a multi-kB
 * policy document through it is felt, and it is absent on most hardware — so it buys a fallback path
 * and an "it depends which one you got" ambiguity for a threat this design does not defend against
 * anyway (the attacker who has root at runtime).
 *
 * **Deliberately not `setUnlockedDeviceRequired(true)`.** The enforcement service samples usage and
 * re-evaluates bedtime while the screen is off and the device is locked. Binding decryption to an
 * unlocked screen would stop exactly the enforcement this product exists to do, and would do it
 * silently.
 *
 * **Deliberately no caller-supplied IV.** `setRandomizedEncryptionRequired(true)` — the default,
 * stated here because it is load-bearing — makes the keystore *refuse* an IV chosen by this code.
 * Nonce reuse under GCM is catastrophic rather than merely weak, and this is the form of the rule
 * that cannot be undone by a later edit to [seal].
 */
class KeystoreSecretCipher(
    private val alias: String = DEFAULT_ALIAS,
    /** Called when a key that existed had to be replaced. See [loadOrGenerate]. */
    private val report: (String) -> Unit = {},
) : SecretCipher {

    override fun seal(context: String, plaintext: ByteArray): ByteArray = withKey { key ->
        val cipher = Cipher.getInstance(TRANSFORMATION)
        cipher.init(Cipher.ENCRYPT_MODE, key)
        cipher.updateAAD(context.toByteArray(Charsets.UTF_8))
        val body = cipher.doFinal(plaintext)
        val iv = cipher.iv
        // Not an assertion about our own code: the IV is chosen by the keystore, and the framing
        // below is what says where the ciphertext starts. A provider that ever returned a different
        // length would produce blobs that open as garbage rather than fail, so it is checked.
        if (iv.size != IV_BYTES) {
            throw GeneralSecurityException("the keystore produced a ${iv.size}-byte IV, not $IV_BYTES")
        }
        iv + body
    }

    override fun open(context: String, sealed: ByteArray): ByteArray {
        if (sealed.size < IV_BYTES + TAG_BITS / 8) {
            throw GeneralSecurityException("sealed value is ${sealed.size} bytes, too short to hold an IV and a tag")
        }
        return withKey { key ->
            val cipher = Cipher.getInstance(TRANSFORMATION)
            cipher.init(Cipher.DECRYPT_MODE, key, GCMParameterSpec(TAG_BITS, sealed, 0, IV_BYTES))
            cipher.updateAAD(context.toByteArray(Charsets.UTF_8))
            cipher.doFinal(sealed, IV_BYTES, sealed.size - IV_BYTES)
        }
    }

    /**
     * Runs [block] with the key, and if the platform says that key is gone, replaces it and runs
     * [block] once more.
     *
     * The cache below is process-wide and holds a *handle*, not the key material. The handle can
     * therefore outlive the keystore entry it names — an alias deleted, a keystore reset, a restore
     * onto different hardware — and from then on the platform answers every `Cipher.init` with
     * `InvalidKeyException: Keystore operation failed` / `KeyStoreException: Key not found`, for as
     * long as the process lives.
     *
     * [loadOrGenerate] has always known how to recover from a missing key. Without this, it could
     * not be reached a second time, so that recovery was unreachable in precisely the situation it
     * was written for — a guard that exists and cannot be called. Measured on API 29: delete the
     * alias under a live instance and every subsequent seal throws, where the intended behaviour is
     * a device that re-enrolls.
     *
     * Exactly one retry. If the second attempt fails too, the key is not merely absent and the
     * exception is the honest answer rather than a loop. A retry on [open] normally ends in
     * `AEADBadTagException` — a different exception, so it cannot recurse — because the blob was
     * sealed under a key that no longer exists; `CipherPreferences` turns that into "absent, and
     * reported", which is the recovery path the storage layer already documents.
     */
    private fun <T> withKey(block: (SecretKey) -> T): T =
        try {
            block(key())
        } catch (gone: InvalidKeyException) {
            KEYS.remove(alias)
            report(
                "the keystore entry '$alias' went away underneath a live key handle " +
                    "(${gone.message}); it is being replaced, and everything sealed under the old " +
                    "key is unreadable",
            )
            block(key())
        }

    /**
     * The key, loaded once per alias and kept until the platform says it is gone — see [withKey].
     *
     * Cached process-wide rather than per instance because there is one instance per preferences
     * file and each `getEntry` is a round trip into the keystore daemon — but mostly because
     * `computeIfAbsent` is what makes generation happen at most once. Two threads reaching a missing
     * alias together and both generating would leave one of them writing under a key the other had
     * already replaced, and nothing about that failure looks like a race afterwards: it reads as a
     * single value that will not open.
     */
    private fun key(): SecretKey = KEYS.computeIfAbsent(alias) { loadOrGenerate(it) }

    /**
     * Loads the alias, or creates it.
     *
     * The second branch is the one worth explaining. An alias that exists but yields no usable
     * secret key means the keystore has lost it — a platform fault, not something this app can
     * repair — and everything sealed under it is *already* unreadable by anyone, including an
     * attacker. Replacing it therefore destroys nothing that was still recoverable, and it is the
     * difference between a device that re-enrolls and a device that throws on every read forever.
     * It is reported rather than done quietly, because "the credential vanished" and "the keystore
     * broke" are not the same incident and only one of them is worth waking up for.
     */
    private fun loadOrGenerate(alias: String): SecretKey {
        val store = KeyStore.getInstance(PROVIDER).apply { load(null) }
        if (store.containsAlias(alias)) {
            val existing = runCatching { store.getEntry(alias, null) }.getOrNull()
            if (existing is KeyStore.SecretKeyEntry) return existing.secretKey
            report(
                "the keystore entry '$alias' exists but yields no usable key; everything sealed " +
                    "under it is unreadable and it is being replaced",
            )
            store.deleteEntry(alias)
        }
        return generate(alias)
    }

    private fun generate(alias: String): SecretKey {
        val generator = KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, PROVIDER)
        generator.init(
            KeyGenParameterSpec.Builder(
                alias,
                KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT,
            )
                .setKeySize(KEY_BITS)
                .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
                .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
                .setRandomizedEncryptionRequired(true)
                .build(),
        )
        return generator.generateKey()
    }

    private companion object {
        const val PROVIDER = "AndroidKeyStore"
        const val TRANSFORMATION = "AES/GCM/NoPadding"
        const val DEFAULT_ALIAS = "familyguard.preferences.v1"
        const val KEY_BITS = 256

        /** GCM's standard nonce length. Anything else costs a hash of the IV inside the cipher. */
        const val IV_BYTES = 12

        /** The full tag. A truncated one is a shorter forgery search for no saving worth having. */
        const val TAG_BITS = 128

        val KEYS = ConcurrentHashMap<String, SecretKey>()
    }
}
