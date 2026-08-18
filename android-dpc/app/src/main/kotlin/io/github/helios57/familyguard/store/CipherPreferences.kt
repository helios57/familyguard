package io.github.helios57.familyguard.store

import android.content.SharedPreferences
import java.util.Base64

/**
 * A [SharedPreferences] whose *values* are sealed by a [SecretCipher], over an ordinary one.
 *
 * Everything here is host-testable Kotlin: the typed encoding, the context strings, and what happens
 * to a value that will not open. The keystore lives behind [SecretCipher] precisely so this file can
 * be driven by a JVM unit test with a cipher that has no keystore in it at all — see
 * `CipherPreferencesTest`.
 *
 * **Keys are stored in the clear, deliberately.** `EncryptedSharedPreferences` encrypted them
 * because it was a general-purpose library where a key could itself be data — a per-account name, an
 * identifier. Every key in this app is a compile-time constant of an open-source project
 * (`credentials`, `input`, `lockout`, `totals`), so encrypting them would hide from an attacker
 * exactly what the repository already tells them, at the cost of a deterministic-encryption scheme
 * that has to be got right. What has to be secret is the *values*, and they are.
 *
 * **Each value is bound to the file and key it was written under.** The context string that goes
 * into the cipher as additional authenticated data is length-prefixed so that no file/key pair can
 * be spelled two ways. Without that binding, an attacker with write access to `/data` could move the
 * sealed credential into the lockout slot, or copy one preference over another, and every blob would
 * still open cleanly. It does not stop putting *yesterday's* value back under the same key; see
 * [SecretCipher] for why that is a different mechanism and is not claimed.
 *
 * **A value that will not open reads as absent, loudly.** Every caller of these stores already
 * documents that an unparseable record is treated as absent so that the device recovers by
 * enrolling again instead of crashing on every boot forever, and a value that will not decrypt is
 * that same case one layer down. Silence is what would make it a false green, so it goes to
 * [onUnreadable] naming the file and the key — the one form of this that a test can assert on, and
 * that a log-free unit test can drive.
 *
 * **A value that will not seal throws.** The asymmetry is the point: failing to read is recoverable
 * by re-fetching, and failing to write is a credential that either never lands or lands in the
 * clear. Neither of those may happen quietly.
 */
class CipherPreferences(
    private val file: String,
    private val delegate: SharedPreferences,
    private val cipher: SecretCipher,
    private val onUnreadable: (String) -> Unit,
) : SharedPreferences {

    override fun getString(key: String, defValue: String?): String? =
        read(key, Tag.STRING)?.let { it } ?: defValue

    override fun getInt(key: String, defValue: Int): Int =
        read(key, Tag.INT)?.toInt() ?: defValue

    override fun getLong(key: String, defValue: Long): Long =
        read(key, Tag.LONG)?.toLong() ?: defValue

    override fun getFloat(key: String, defValue: Float): Float =
        read(key, Tag.FLOAT)?.toFloat() ?: defValue

    override fun getBoolean(key: String, defValue: Boolean): Boolean =
        read(key, Tag.BOOLEAN)?.toBooleanStrict() ?: defValue

    /**
     * Unsupported, on purpose, in both directions.
     *
     * Nothing in this app stores a set, and a half-done encoding — one that works until a stored
     * string contains the separator — is the kind of thing that passes every test written the day it
     * was added. Refusing outright fails at the first call, in the caller's own stack, with a
     * sentence saying what to do instead.
     */
    override fun getStringSet(key: String, defValues: MutableSet<String>?): MutableSet<String> =
        throw UnsupportedOperationException(UNSUPPORTED_SET)

    override fun contains(key: String): Boolean = delegate.contains(key)

    /**
     * Every entry that opens, decoded back to its stored type.
     *
     * Entries that do not open are reported and left out rather than surfaced as some placeholder:
     * this is the method a future diagnostic screen would use, and a map that mixes recovered values
     * with markers for unrecovered ones invites the caller to treat them alike.
     */
    override fun getAll(): MutableMap<String, Any?> {
        val out = LinkedHashMap<String, Any?>()
        for (key in delegate.all.keys) {
            val plain = open(key) ?: continue
            val tag = Tag.of(plain) ?: run {
                onUnreadable("$file/$key opened but carries no type tag")
                continue
            }
            val body = plain.substring(1)
            out[key] = when (tag) {
                Tag.STRING -> body
                Tag.INT -> body.toInt()
                Tag.LONG -> body.toLong()
                Tag.FLOAT -> body.toFloat()
                Tag.BOOLEAN -> body.toBooleanStrict()
            }
        }
        return out
    }

    override fun edit(): SharedPreferences.Editor = Sealing(delegate.edit())

    override fun registerOnSharedPreferenceChangeListener(
        listener: SharedPreferences.OnSharedPreferenceChangeListener,
    ) = delegate.registerOnSharedPreferenceChangeListener(listener)

    override fun unregisterOnSharedPreferenceChangeListener(
        listener: SharedPreferences.OnSharedPreferenceChangeListener,
    ) = delegate.unregisterOnSharedPreferenceChangeListener(listener)

    /**
     * The stored body for [key] if it opens *and* carries [expected]; null if it is absent.
     *
     * A tag mismatch throws `ClassCastException`, which is what a real `SharedPreferences` does when
     * a key holding a long is read with `getString`. Matching that matters more than it looks:
     * answering the default instead would turn a caller's type error into a silently missing value.
     */
    private fun read(key: String, expected: Tag): String? {
        val plain = open(key) ?: return null
        val tag = Tag.of(plain)
        if (tag == null) {
            onUnreadable("$file/$key opened but carries no type tag")
            return null
        }
        if (tag != expected) {
            throw ClassCastException("$file/$key holds a ${tag.name.lowercase()}, read as a ${expected.name.lowercase()}")
        }
        return plain.substring(1)
    }

    /** The decrypted text at [key], or null when it is absent or unreadable. */
    private fun open(key: String): String? {
        val stored = delegate.getString(key, null) ?: return null
        return runCatching {
            String(cipher.open(context(key), Base64.getDecoder().decode(stored)), Charsets.UTF_8)
        }.getOrElse {
            onUnreadable("$file/$key is stored but will not open (${it.javaClass.simpleName}); read as absent")
            null
        }
    }

    private fun seal(key: String, tag: Tag, body: String): String =
        Base64.getEncoder().withoutPadding().encodeToString(
            cipher.seal(context(key), (tag.marker + body).toByteArray(Charsets.UTF_8)),
        )

    /**
     * The authenticated context for one entry: the file and the key, spelled exactly one way.
     *
     * Length-prefixed rather than separated by a character, so that no pair of (file, key) can
     * collide with another by containing the separator. The keys here are constants and could not,
     * today; a scheme that is only safe because of what the callers happen to pass is a scheme that
     * breaks on the first caller who passes something else.
     */
    private fun context(key: String): String = "${file.length}:$file:$key"

    private inner class Sealing(private val editor: SharedPreferences.Editor) : SharedPreferences.Editor {

        override fun putString(key: String, value: String?): SharedPreferences.Editor = apply {
            // Mirrors the platform: putString(key, null) is a removal, not a stored null.
            if (value == null) editor.remove(key) else editor.putString(key, seal(key, Tag.STRING, value))
        }

        override fun putInt(key: String, value: Int): SharedPreferences.Editor = apply {
            editor.putString(key, seal(key, Tag.INT, value.toString()))
        }

        override fun putLong(key: String, value: Long): SharedPreferences.Editor = apply {
            editor.putString(key, seal(key, Tag.LONG, value.toString()))
        }

        override fun putFloat(key: String, value: Float): SharedPreferences.Editor = apply {
            editor.putString(key, seal(key, Tag.FLOAT, value.toString()))
        }

        override fun putBoolean(key: String, value: Boolean): SharedPreferences.Editor = apply {
            editor.putString(key, seal(key, Tag.BOOLEAN, value.toString()))
        }

        override fun putStringSet(key: String, values: MutableSet<String>?): SharedPreferences.Editor =
            throw UnsupportedOperationException(UNSUPPORTED_SET)

        override fun remove(key: String): SharedPreferences.Editor = apply { editor.remove(key) }

        override fun clear(): SharedPreferences.Editor = apply { editor.clear() }

        override fun commit(): Boolean = editor.commit()

        override fun apply() = editor.apply()
    }

    /**
     * The stored type, as a single leading character of the plaintext.
     *
     * Inside the sealed blob rather than beside it, so that it is authenticated too: a tag an
     * attacker could edit would let a stored `false` be re-read as the string "false", and every
     * boolean in this app is a decision about whether something is enforced.
     */
    private enum class Tag(val marker: Char) {
        STRING('S'),
        INT('I'),
        LONG('L'),
        FLOAT('F'),
        BOOLEAN('B'),
        ;

        companion object {
            fun of(plain: String): Tag? =
                plain.firstOrNull()?.let { first -> entries.firstOrNull { it.marker == first } }
        }
    }

    private companion object {
        const val UNSUPPORTED_SET =
            "CipherPreferences stores no string sets; serialise the collection and store it as a string"
    }
}
