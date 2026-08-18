package io.github.helios57.familyguard.recovery

import io.github.helios57.familyguard.net.RecoveryMaterial
import java.security.MessageDigest
import java.util.Base64
import javax.crypto.SecretKeyFactory
import javax.crypto.spec.PBEKeySpec

/**
 * The offline recovery code (FR-12), folded and verified the way the server folds and verifies it.
 *
 * The device never holds the code. What enrollment hands it is `salt`, `iterations` and `hash` —
 * the same three values the server derived from the plaintext it showed the parent once. So the
 * check here is a re-derivation, and the only thing that makes it a check rather than a coincidence
 * is that both sides agree, character for character, on what the typed string *is*.
 *
 * That agreement is the whole risk. `backend/internal/auth/tokens.go` is the authority; this file
 * mirrors it, and `recovery-vectors.json` — hand-written expectations plus digests from a third,
 * independent PBKDF2 implementation, replayed by both suites — is what keeps the mirror honest.
 * Vectors taken from either side's output would only assert that each side does what it does.
 * Guessing at the mirror is not an option either: a normaliser
 * that folds one character differently produces a device that rejects the code printed on the
 * parent's screen, on a phone that is by then offline and locked down. There is no second chance
 * and no way to debug it from the console.
 */
object RecoveryCode {

    /**
     * Folds user input to the canonical form: uppercase, then drop every separator — space or dash,
     * wherever it appears. A parent copying twenty characters off another screen should not fail on
     * formatting, on a phone that is by then locked down and possibly offline.
     *
     * Three details are mirrored deliberately rather than left to the obvious Kotlin call, because
     * the obvious call differs from Go and the difference is invisible until it is a bricked phone:
     *
     * - **Spaces.** `String.trim()` and `Char.isWhitespace()` go by `Character.isWhitespace`, which
     *   *excludes* the non-breaking space (U+00A0) that Go's `unicode.IsSpace` includes, and
     *   *includes* the four ASCII separators (U+001C..U+001F) that Go excludes. [GO_SPACE] is Go's
     *   set, spelled out; the Java predicate would differ at both ends.
     * - **Uppercase.** `String.uppercase()` does *full* case mapping, so `ß` becomes `SS`; Go's
     *   `strings.ToUpper` does simple mapping and leaves it alone. `Character.toUpperCase(int)` is
     *   the simple mapping, over the same Unicode table.
     * - **Order.** Go uppercases first and strips afterwards, so the switch sees uppercased runes.
     *   Stripping first would be identical today and is not guaranteed to stay that way.
     *
     * None of this matters for a code the server generated — that alphabet is 30 ASCII characters.
     * It matters for what a parent's keyboard puts in front of it: an autocorrected en-dash, a
     * non-breaking space pasted from a chat message, a soft hyphen nobody can see, a trailing
     * newline. Nothing is given away by being generous: the alphabet holds no space and no dash, so
     * a string that folds to the right code is the right code differently punctuated, never a
     * different one.
     */
    fun normalize(raw: String): String {
        val folded = StringBuilder(raw.length)
        var i = 0
        while (i < raw.length) {
            val point = raw.codePointAt(i)
            i += Character.charCount(point)
            val upper = Character.toUpperCase(point)
            if (isSeparator(upper)) continue
            folded.appendCodePoint(upper)
        }
        return folded.toString()
    }

    /** `isRecoverySeparator` in `backend/internal/auth/tokens.go`, character for character. */
    private fun isSeparator(codePoint: Int): Boolean =
        codePoint in GO_SPACE || codePoint in EN_QUAD..HAIR_SPACE || codePoint in DASHES

    /**
     * `unicode.IsSpace`, as a set. Latin-1 is the explicit switch in Go's implementation; above it
     * the `White_Space` table contributes U+1680, U+2000..U+200A, U+2028, U+2029, U+202F, U+205F
     * and U+3000. The U+2000..U+200A run is a range beside this set, not ten more entries.
     *
     * Written as code points rather than as the characters themselves: a set literal of things
     * that all look like a blank is a line no reviewer can check, and one of them becoming an
     * ordinary space in an editor would be invisible in the diff.
     */
    private val GO_SPACE: Set<Int> = setOf(
        '\t'.code, '\n'.code, 0x000B, 0x000C, '\r'.code, ' '.code, 0x0085, 0x00A0,
        0x1680, 0x2028, 0x2029, 0x202F, 0x205F, 0x3000,
    )

    /**
     * The dashes Go's `isRecoverySeparator` enumerates, in the same order.
     *
     * A list and not `Character.getType(cp) == DASH_PUNCTUATION`, for the reason recorded there:
     * Go and an Android runtime ship different Unicode table versions, and a category membership
     * that differs between them is a code the console accepts and the phone rejects. An enumerated
     * set cannot drift. The same rule as above applies and matters more here — U+00AD is invisible,
     * and half of the rest are a hyphen in a monospace font.
     */
    private val DASHES: Set<Int> = setOf(
        '-'.code, // U+002D HYPHEN-MINUS — what the server writes between the groups
        0x00AD, // SOFT HYPHEN
        0x2010, // HYPHEN
        0x2011, // NON-BREAKING HYPHEN
        0x2012, // FIGURE DASH
        0x2013, // EN DASH
        0x2014, // EM DASH
        0x2015, // HORIZONTAL BAR
        0x2212, // MINUS SIGN
        0xFE58, // SMALL EM DASH
        0xFE63, // SMALL HYPHEN-MINUS
        0xFF0D, // FULLWIDTH HYPHEN-MINUS
    )

    private const val EN_QUAD = 0x2000
    private const val HAIR_SPACE = 0x200A
}

/**
 * PBKDF2 over the normalized code. Separate from [RecoveryVerifier] so the verifier's decisions —
 * what it refuses before deriving anything — can be tested without a JCE provider, and so a
 * derivation that throws can be reproduced.
 */
fun interface RecoveryDerivation {
    /** @throws Exception if the platform cannot derive; the verifier turns that into a denial. */
    fun derive(normalizedCode: String, salt: ByteArray, iterations: Int): ByteArray
}

/**
 * `PBKDF2WithHmacSHA256`, 256-bit output — `pbkdf2.Key(sha256.New, code, salt, iterations, 32)`.
 *
 * The password reaches the JCE as a `char[]`, and providers disagree about how they turn one into
 * bytes: the JDK's encodes UTF-8, Bouncy Castle's PKCS#5 v2 scheme has historically taken the low
 * byte of each character. Go encodes UTF-8. The two agree for every ASCII character and can differ
 * above it — so as long as every character a generated code can contain is ASCII, the question
 * cannot arise. The guard that keeps that true is `TestRecoveryAlphabetIsAscii` in
 * `backend/internal/auth/recovery_vectors_test.go`, and it lives there rather than here on purpose:
 * the alphabet is the server's, and a test in this repository could only assert against a copy. It
 * fails if a character above U+007F is added, if one repeats, if one is a character normalisation
 * strips, or if the read-aloud exclusions (`I L O U 0 1`) are quietly restored.
 */
object Pbkdf2HmacSha256 : RecoveryDerivation {
    override fun derive(normalizedCode: String, salt: ByteArray, iterations: Int): ByteArray {
        val spec = PBEKeySpec(normalizedCode.toCharArray(), salt, iterations, KEY_BITS)
        try {
            return SecretKeyFactory.getInstance(ALGORITHM).generateSecret(spec).encoded
        } finally {
            // The spec holds its own copy of the characters. Clearing it does not make the code
            // unrecoverable from memory — the String it came from is still interned somewhere — but
            // it costs nothing and shortens the window.
            spec.clearPassword()
        }
    }

    private const val ALGORITHM = "PBKDF2WithHmacSHA256"
    private const val KEY_BITS = 32 * 8
}

/**
 * Decides whether a typed string is this device's recovery code.
 *
 * Every path that is not a successful comparison denies. There is no branch where a missing salt,
 * an unparseable hash, a zero iteration count or a provider that throws results in a device that
 * unlocks: the failure of the check is not evidence about the code.
 *
 * The mirror of that: [usable] exists so the UI can say *why* nothing will ever be accepted, rather
 * than showing a prompt that rejects every code a parent types. A device enrolled before the server
 * issued recovery material has no material, and telling the person holding it to try again is worse
 * than telling them the truth.
 */
class RecoveryVerifier(
    private val material: RecoveryMaterial,
    private val derive: RecoveryDerivation = Pbkdf2HmacSha256,
) {

    private val salt: ByteArray? = decode(material.salt)
    private val expected: ByteArray? = decode(material.hash)

    /** Whether this device holds material a code could ever match. */
    fun usable(): Boolean =
        salt != null && salt.isNotEmpty() &&
            expected != null && expected.isNotEmpty() &&
            material.iterations >= 1

    fun verify(entered: String): Boolean {
        if (!usable()) return false
        val normalized = RecoveryCode.normalize(entered)
        // Not a shortcut: an empty password is rejected outright by some providers and accepted by
        // others, so deriving from one would make "typed nothing" mean different things on
        // different phones. Here it means no.
        if (normalized.isEmpty()) return false

        val derived = try {
            derive.derive(normalized, salt!!, material.iterations)
        } catch (_: Exception) {
            return false
        }
        // Time-constant since it was introduced; the ordinary `contentEquals` returns on the first
        // differing byte, which over a few thousand attempts is a readable side channel.
        return MessageDigest.isEqual(derived, expected!!)
    }

    private fun decode(value: String): ByteArray? {
        if (value.isEmpty()) return null
        return try {
            // RawURL on the wire: the server encodes with `base64.RawURLEncoding`, which omits the
            // padding this decoder tolerates.
            Base64.getUrlDecoder().decode(value)
        } catch (_: IllegalArgumentException) {
            null
        }
    }
}
