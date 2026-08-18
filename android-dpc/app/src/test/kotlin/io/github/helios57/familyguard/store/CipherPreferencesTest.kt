package io.github.helios57.familyguard.store

import android.content.SharedPreferences
import java.security.GeneralSecurityException
import java.util.Base64
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertThrows
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * [CipherPreferences] driven on the host, with a cipher that has no keystore in it.
 *
 * The split exists for this test. What can go wrong in a preferences layer is not the AES — that is
 * the platform's — it is the bookkeeping around it: a value that opens under the wrong key, a type
 * read back as another type, a failure to decrypt that reads as "never stored". None of those need a
 * device, and none of them would be noticed by a test that only checked a round trip.
 *
 * [FakeCipher] is deliberately not a no-op. It binds its context and it transforms its input, so the
 * "not stored in the clear" and "cannot be moved" checks below would fail against a cipher that did
 * nothing — which is the only way those two checks are worth anything. [fakeCipherRefusesAForeignContext]
 * pins that property directly, so a later edit that weakens the fake fails there instead of quietly
 * hollowing out four other tests.
 */
class CipherPreferencesTest {

    private val delegate = FakePreferences()
    private val reports = mutableListOf<String>()
    private val prefs = CipherPreferences("family-guard-test", delegate, FakeCipher(), reports::add)

    @Test
    fun roundTripsEveryStoredType() {
        prefs.edit()
            .putString("s", "hunter2")
            .putInt("i", -7)
            .putLong("l", Long.MIN_VALUE)
            .putFloat("f", 1.5f)
            .putBoolean("b", true)
            .commit()

        assertEquals("hunter2", prefs.getString("s", null))
        assertEquals(-7, prefs.getInt("i", 0))
        assertEquals(Long.MIN_VALUE, prefs.getLong("l", 0L))
        assertEquals(1.5f, prefs.getFloat("f", 0f), 0f)
        assertTrue(prefs.getBoolean("b", false))
        assertTrue(reports.isEmpty())
    }

    @Test
    fun absentKeysAnswerTheDefault() {
        assertNull(prefs.getString("nothing", null))
        assertEquals("fallback", prefs.getString("nothing", "fallback"))
        assertEquals(42L, prefs.getLong("nothing", 42L))
        assertFalse(prefs.contains("nothing"))
        assertTrue(reports.isEmpty())
    }

    @Test
    fun storesNothingInTheClear() {
        prefs.edit().putString("credentials", SECRET).commit()

        val stored = requireNonNull(delegate.raw["credentials"])
        assertFalse("the ciphertext is the plaintext", stored.contains(SECRET))
        val decoded = String(Base64.getDecoder().decode(stored), Charsets.ISO_8859_1)
        assertFalse("the plaintext survives base64 decoding", decoded.contains(SECRET))
    }

    @Test
    fun aValueCannotBeMovedToAnotherKey() {
        prefs.edit().putString("credentials", SECRET).commit()
        delegate.raw["lockout"] = requireNonNull(delegate.raw["credentials"])

        assertNull("a blob copied to another key opened", prefs.getString("lockout", null))
        assertEquals("the untouched key stopped working too", SECRET, prefs.getString("credentials", null))
        assertEquals(1, reports.size)
        assertTrue(reports.single(), reports.single().contains("family-guard-test/lockout"))
    }

    @Test
    fun aValueCannotBeMovedToAnotherFile() {
        // Same underlying store, a different file name: what an attacker gets by renaming a file in
        // /data, and what a future caller gets by passing the wrong name to `encryptedPreferences`.
        val other = CipherPreferences("family-guard-other", delegate, FakeCipher(), reports::add)
        prefs.edit().putString("credentials", SECRET).commit()

        assertNull("a blob read under another file name opened", other.getString("credentials", null))
        assertEquals(SECRET, prefs.getString("credentials", null))
    }

    @Test
    fun anUnopenableValueReadsAsAbsentAndIsReported() {
        delegate.raw["credentials"] = Base64.getEncoder().encodeToString("not sealed by anything".toByteArray())

        assertNull(prefs.getString("credentials", null))
        assertEquals("fallback", prefs.getString("credentials", "fallback"))
        assertEquals(2, reports.size)
        assertTrue(reports.first(), reports.first().contains("family-guard-test/credentials"))
        assertTrue(reports.first(), reports.first().contains("will not open"))
    }

    @Test
    fun aValueThatIsNotEvenBase64ReadsAsAbsentAndIsReported() {
        delegate.raw["credentials"] = "!!! not base64 !!!"

        assertNull(prefs.getString("credentials", null))
        assertEquals(1, reports.size)
    }

    @Test
    fun readingAStoredTypeAsAnotherThrows() {
        prefs.edit().putLong("applied_version", 9L).commit()

        val thrown = assertThrows(ClassCastException::class.java) { prefs.getString("applied_version", null) }
        assertTrue(thrown.message!!, thrown.message!!.contains("holds a long"))
        // The platform's own behaviour, and the reason it is worth matching: answering the default
        // would turn a caller's type error into a value that had silently never been written.
        assertEquals(9L, prefs.getLong("applied_version", 0L))
    }

    @Test
    fun putStringNullRemoves() {
        prefs.edit().putString("credentials", SECRET).commit()
        prefs.edit().putString("credentials", null).commit()

        assertFalse(prefs.contains("credentials"))
        assertNull(prefs.getString("credentials", null))
    }

    @Test
    fun removeAndClearReachTheStore() {
        prefs.edit().putString("a", "1").putString("b", "2").commit()
        prefs.edit().remove("a").commit()
        assertFalse(prefs.contains("a"))
        assertTrue(prefs.contains("b"))

        prefs.edit().clear().commit()
        assertTrue(delegate.raw.isEmpty())
    }

    @Test
    fun getAllReturnsEveryEntryDecodedToItsStoredType() {
        prefs.edit().putString("s", "text").putLong("l", 5L).putBoolean("b", false).commit()

        assertEquals(mapOf("s" to "text", "l" to 5L, "b" to false), prefs.all)
    }

    @Test
    fun getAllSkipsAndReportsWhatWillNotOpen() {
        prefs.edit().putString("good", "text").commit()
        delegate.raw["bad"] = Base64.getEncoder().encodeToString("garbage".toByteArray())

        assertEquals(mapOf<String, Any?>("good" to "text"), prefs.all)
        assertEquals(1, reports.size)
    }

    @Test
    fun stringSetsAreRefusedInBothDirections() {
        assertThrows(UnsupportedOperationException::class.java) { prefs.getStringSet("k", null) }
        assertThrows(UnsupportedOperationException::class.java) {
            prefs.edit().putStringSet("k", mutableSetOf("v"))
        }
    }

    @Test
    fun commitReportsWhatTheStoreReports() {
        assertTrue(prefs.edit().putString("k", "v").commit())
        delegate.commitSucceeds = false
        assertFalse(prefs.edit().putString("k", "v").commit())
    }

    @Test
    fun aFailureToSealThrowsRatherThanStoringAnything() {
        val breaking = CipherPreferences("family-guard-test", delegate, BrokenCipher, reports::add)

        assertThrows(GeneralSecurityException::class.java) { breaking.edit().putString("credentials", SECRET) }
        assertTrue("a value that could not be sealed was stored anyway", delegate.raw.isEmpty())
    }

    /** The negative control for [FakeCipher]: without this property, four tests above prove nothing. */
    @Test
    fun fakeCipherRefusesAForeignContext() {
        val cipher = FakeCipher()
        val sealed = cipher.seal("one", "plaintext".toByteArray())

        assertEquals("plaintext", String(cipher.open("one", sealed)))
        assertThrows(GeneralSecurityException::class.java) { cipher.open("two", sealed) }
    }

    private fun <T : Any> requireNonNull(value: T?): T = value ?: throw AssertionError("nothing was stored")

    private companion object {
        /**
         * Deliberately readable rather than credential-shaped.
         *
         * Every assertion that uses it is an exact substring check, so entropy buys the tests
         * nothing — while a high-entropy base64 blob here is indistinguishable from a real token to
         * a secret scanner. The first version of this constant was exactly that, and `gitleaks`
         * reported it as a `generic-api-key` finding. The choice then is an allowlist entry or a
         * value that does not need one, and an allowlist entry is a place a genuine secret can
         * later hide. So: leave it low-entropy and obviously fake.
         */
        const val SECRET = "not-a-real-token-device-credential-fixture"
    }
}

/**
 * A cipher with no keystore: XOR framing plus an authenticated context.
 *
 * Not encryption, and not pretending to be. What it has to reproduce is the only two properties
 * [CipherPreferences] relies on — the stored form is not the plaintext, and a value only opens under
 * the context it was sealed with — so that the tests above measure the bookkeeping rather than the
 * cipher. The real one is measured on a device, in `KeystoreSecretCipherTest`.
 */
private class FakeCipher : SecretCipher {
    override fun seal(context: String, plaintext: ByteArray): ByteArray =
        scramble(context.toByteArray(Charsets.UTF_8) + 0 + plaintext)

    override fun open(context: String, sealed: ByteArray): ByteArray {
        val body = scramble(sealed)
        val marker = context.toByteArray(Charsets.UTF_8) + 0
        if (body.size < marker.size || !body.copyOf(marker.size).contentEquals(marker)) {
            throw GeneralSecurityException("sealed under a different context")
        }
        return body.copyOfRange(marker.size, body.size)
    }

    private fun scramble(bytes: ByteArray) = ByteArray(bytes.size) { (bytes[it].toInt() xor 0x5A).toByte() }
}

private object BrokenCipher : SecretCipher {
    override fun seal(context: String, plaintext: ByteArray): ByteArray =
        throw GeneralSecurityException("no key")

    override fun open(context: String, sealed: ByteArray): ByteArray =
        throw GeneralSecurityException("no key")
}

/** An in-memory `SharedPreferences`. Only the string surface is reachable through the wrapper. */
private class FakePreferences : SharedPreferences {
    val raw = linkedMapOf<String, String>()
    var commitSucceeds = true

    override fun getAll(): MutableMap<String, *> = LinkedHashMap<String, Any?>(raw)
    override fun getString(key: String, defValue: String?): String? = raw[key] ?: defValue
    override fun contains(key: String): Boolean = raw.containsKey(key)
    override fun edit(): SharedPreferences.Editor = Editor()

    override fun getStringSet(key: String, defValues: MutableSet<String>?) = unreached()
    override fun getInt(key: String, defValue: Int) = unreached()
    override fun getLong(key: String, defValue: Long) = unreached()
    override fun getFloat(key: String, defValue: Float) = unreached()
    override fun getBoolean(key: String, defValue: Boolean) = unreached()
    override fun registerOnSharedPreferenceChangeListener(l: SharedPreferences.OnSharedPreferenceChangeListener) = unreached()
    override fun unregisterOnSharedPreferenceChangeListener(l: SharedPreferences.OnSharedPreferenceChangeListener) = unreached()

    /** Everything typed goes through the wrapper's own encoding; reaching one of these is the bug. */
    private fun unreached(): Nothing =
        throw AssertionError("CipherPreferences must store every value as a sealed string")

    private inner class Editor : SharedPreferences.Editor {
        private val pending = linkedMapOf<String, String?>()
        private var clearing = false

        override fun putString(key: String, value: String?) = apply { pending[key] = value }
        override fun remove(key: String) = apply { pending[key] = null }
        override fun clear() = apply { clearing = true }

        override fun commit(): Boolean {
            if (clearing) raw.clear()
            pending.forEach { (key, value) -> if (value == null) raw.remove(key) else raw[key] = value }
            return commitSucceeds
        }

        override fun apply() {
            commit()
        }

        override fun putStringSet(key: String, values: MutableSet<String>?) = unreached()
        override fun putInt(key: String, value: Int) = unreached()
        override fun putLong(key: String, value: Long) = unreached()
        override fun putFloat(key: String, value: Float) = unreached()
        override fun putBoolean(key: String, value: Boolean) = unreached()
    }
}
