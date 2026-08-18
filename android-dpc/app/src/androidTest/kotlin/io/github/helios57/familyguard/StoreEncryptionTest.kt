package io.github.helios57.familyguard

import android.content.Context
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import io.github.helios57.familyguard.store.KeystoreSecretCipher
import io.github.helios57.familyguard.store.encryptedPreferences
import java.io.File
import java.security.GeneralSecurityException
import java.security.KeyStore
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertThrows
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith

/**
 * The half of the storage layer that only a device can answer: the keystore, and the file on disk.
 *
 * `CipherPreferencesTest` drives everything above [KeystoreSecretCipher] on the host with a cipher
 * that has no keystore in it, which is the right place for the bookkeeping and the wrong place for
 * this. Here the questions are whether the platform really produces a fresh nonce per seal, whether
 * a key generated with no authentication binding is usable from a background service, whether the
 * key outlives the object that made it — and, the one that matters most to a parent, whether the
 * file left in `/data` is actually unreadable.
 *
 * It uses its own alias and its own preferences file. A test that generated, used and deleted the
 * *production* alias would wipe the credential of whatever device it ran on.
 */
@RunWith(AndroidJUnit4::class)
class StoreEncryptionTest {

    private val context: Context = InstrumentationRegistry.getInstrumentation().targetContext
    private val cipher = KeystoreSecretCipher(ALIAS)

    @After
    fun removeTheTestKeyAndFile() {
        KeyStore.getInstance(PROVIDER).apply { load(null) }.deleteEntry(ALIAS)
        context.deleteSharedPreferences(FILE)
    }

    @Test
    fun sealsAndOpensUnderTheSameContext() {
        val sealed = cipher.seal(CONTEXT, PLAINTEXT.toByteArray())

        assertEquals(PLAINTEXT, String(cipher.open(CONTEXT, sealed)))
    }

    /**
     * The single most important property of the whole file, and the one a hand-written IV would
     * quietly destroy: two seals of the same bytes must not produce the same blob. Under GCM a
     * repeated nonce does not weaken the ciphertext, it hands over the key stream *and* the
     * authentication key.
     */
    @Test
    fun everySealUsesAFreshNonce() {
        val first = cipher.seal(CONTEXT, PLAINTEXT.toByteArray())
        val second = cipher.seal(CONTEXT, PLAINTEXT.toByteArray())

        assertNotEquals(first.toList(), second.toList())
        assertNotEquals(first.take(12), second.take(12))
        assertEquals(PLAINTEXT, String(cipher.open(CONTEXT, second)))
    }

    @Test
    fun refusesAForeignContext() {
        val sealed = cipher.seal(CONTEXT, PLAINTEXT.toByteArray())

        assertThrows(GeneralSecurityException::class.java) { cipher.open("$CONTEXT-other", sealed) }
    }

    @Test
    fun refusesAnEditedBlob() {
        val sealed = cipher.seal(CONTEXT, PLAINTEXT.toByteArray())
        val tampered = sealed.copyOf().also { it[it.size - 1] = (it[it.size - 1] + 1).toByte() }
        val truncated = sealed.copyOf(sealed.size - 1)

        assertThrows(GeneralSecurityException::class.java) { cipher.open(CONTEXT, tampered) }
        assertThrows(GeneralSecurityException::class.java) { cipher.open(CONTEXT, truncated) }
        assertThrows(GeneralSecurityException::class.java) { cipher.open(CONTEXT, ByteArray(0)) }
    }

    /**
     * The key is in the keystore, not in this object.
     *
     * Asserted against the keystore rather than by opening with a second instance, because the
     * cipher caches by alias for the life of the process: a second instance would hit that cache and
     * the test would pass without anything ever having been persisted.
     */
    @Test
    fun theKeyIsPersistedInTheKeystore() {
        cipher.seal(CONTEXT, PLAINTEXT.toByteArray())

        val store = KeyStore.getInstance(PROVIDER).apply { load(null) }
        assertTrue("nothing was written to the keystore under $ALIAS", store.containsAlias(ALIAS))
        assertTrue(store.getEntry(ALIAS, null) is KeyStore.SecretKeyEntry)
    }

    /**
     * What a pulled `/data` image gives up.
     *
     * This is the claim the whole storage layer exists to make, and it is the one no host test can
     * check: `commit()` returning true says a file was written, not what is in it.
     */
    @Test
    fun theFileOnDiskDoesNotContainTheSecret() {
        val preferences = encryptedPreferences(context, FILE)
        assertTrue(preferences.edit().putString(KEY, PLAINTEXT).commit())
        assertEquals(PLAINTEXT, preferences.getString(KEY, null))

        val onDisk = File(File(context.applicationInfo.dataDir, "shared_prefs"), "$FILE.xml")
        assertTrue("no preferences file at $onDisk; this test would otherwise pass by reading nothing", onDisk.isFile)
        val text = onDisk.readText()
        assertTrue("the file is empty", text.isNotEmpty())
        // The key is stored in the clear by design; the value must not be.
        assertTrue("the scan found nothing to scan", text.contains(KEY))
        assertFalse("the secret is in the file on disk", text.contains(PLAINTEXT))
    }

    /** A file whose values were sealed for another file name reads as empty, not as data. */
    @Test
    fun aFileCannotBeRenamedIntoAnother() {
        val preferences = encryptedPreferences(context, FILE)
        assertTrue(preferences.edit().putString(KEY, PLAINTEXT).commit())

        val source = File(File(context.applicationInfo.dataDir, "shared_prefs"), "$FILE.xml")
        val target = File(source.parentFile, "$OTHER_FILE.xml")
        target.writeText(source.readText())
        try {
            assertNull(encryptedPreferences(context, OTHER_FILE).getString(KEY, null))
        } finally {
            context.deleteSharedPreferences(OTHER_FILE)
            target.delete()
        }
    }

    /**
     * A key that disappears underneath a live cipher must be replaced, not wedge the instance.
     *
     * [KeystoreSecretCipher] caches the loaded key process-wide, and the cache is what makes
     * generation happen at most once. It also means a `SecretKey` handle can outlive the keystore
     * entry it refers to — a deleted alias, a keystore reset, a restore onto a different device —
     * and the platform then answers every `Cipher.init` with `Key not found` for as long as the
     * process lives. `loadOrGenerate` already knows how to recover from a missing key; before this
     * test it could never be reached a second time, so the recovery it documents was unreachable in
     * exactly the situation it was written for.
     *
     * The old blob is deliberately NOT recoverable afterwards. It was sealed under a key that no
     * longer exists anywhere, so refusing to open it is the honest answer, and `CipherPreferences`
     * turns that refusal into "absent, and reported" rather than a crash.
     */
    @Test
    fun aKeyDeletedUnderneathTheCipherIsReplacedRatherThanWedgingIt() {
        val replaced = mutableListOf<String>()
        val subject = KeystoreSecretCipher(ALIAS) { replaced += it }

        val before = subject.seal(CONTEXT, PLAINTEXT.toByteArray())
        assertEquals(PLAINTEXT, String(subject.open(CONTEXT, before)))

        KeyStore.getInstance(PROVIDER).apply { load(null) }.deleteEntry(ALIAS)

        // Same instance, same alias, cached handle now dangling.
        val after = subject.seal(CONTEXT, PLAINTEXT.toByteArray())
        assertEquals(PLAINTEXT, String(subject.open(CONTEXT, after)))
        assertTrue("the key was replaced without saying so", replaced.isNotEmpty())
        assertThrows(GeneralSecurityException::class.java) { subject.open(CONTEXT, before) }
    }

    private companion object {
        const val PROVIDER = "AndroidKeyStore"
        const val ALIAS = "familyguard.test.storeencryption"
        const val FILE = "family-guard-store-encryption-test"
        const val OTHER_FILE = "family-guard-store-encryption-test-other"
        const val KEY = "credentials"
        const val CONTEXT = "20:family-guard-secret:credentials"
        const val PLAINTEXT = "{\"token\":\"the-device-token\",\"server\":\"https://example.invalid\"}"
    }
}
