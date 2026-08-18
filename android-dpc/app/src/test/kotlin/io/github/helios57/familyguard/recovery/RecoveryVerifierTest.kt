package io.github.helios57.familyguard.recovery

import io.github.helios57.familyguard.net.RecoveryMaterial
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import java.util.Base64

/**
 * What [RecoveryVerifier] decides *before* it derives anything, and what it does when the derivation
 * itself goes wrong.
 *
 * `RecoveryVectorsTest` covers the agreement between this and the server: a code that folds one way
 * here and another way there. It cannot cover this, and the difference was measured rather than
 * assumed — calibration broke [RecoveryVerifier.usable] into accepting a zero work factor and every
 * vector stayed green, because `PBEKeySpec` refuses a non-positive iteration count on its own and
 * the verifier catches the exception. The answer never changed, so nothing was wrong with the
 * vectors; they simply do not measure this.
 *
 * They cannot, either. [usable] has a second job no vector can see: telling the screen *why* nothing
 * will ever be accepted, so a parent holding a phone with no recovery material is told that instead
 * of being shown a prompt that rejects every code they type. A vector file only knows about answers.
 *
 * Every case here injects a [RecoveryDerivation], for two reasons. It runs without a JCE provider,
 * and — the reason that matters — a fake can be *asked whether it was called*. "Refused without
 * deriving anything" is a stronger statement than "refused", and it is the one the guards actually
 * make: a refusal that still ran 120 000 rounds of PBKDF2 over a blank string would pass an
 * assertion about the return value alone.
 */
class RecoveryVerifierTest {

    /** Records what it was asked, and answers however the test tells it to. */
    private class FakeDerivation(
        private val answer: (String, ByteArray, Int) -> ByteArray,
    ) : RecoveryDerivation {
        var calls = 0
            private set
        var lastCode: String? = null
            private set
        var lastSalt: ByteArray? = null
            private set
        var lastIterations: Int? = null
            private set

        override fun derive(normalizedCode: String, salt: ByteArray, iterations: Int): ByteArray {
            calls++
            lastCode = normalizedCode
            lastSalt = salt
            lastIterations = iterations
            return answer(normalizedCode, salt, iterations)
        }
    }

    private fun matching() = FakeDerivation { _, _, _ -> HASH_BYTES }

    private fun verifier(
        material: RecoveryMaterial,
        derivation: RecoveryDerivation,
    ) = RecoveryVerifier(material, derivation)

    // ---- material a code could never match ---------------------------------------------------

    @Test
    fun `material with no salt can never accept anything`() {
        val derivation = matching()
        val verifier = verifier(RecoveryMaterial(salt = "", iterations = ITERATIONS, hash = HASH), derivation)

        assertFalse("a device with no salt reported usable recovery material", verifier.usable())
        assertFalse(verifier.verify(CODE))
        assertEquals("a code was derived against material that cannot match", 0, derivation.calls)
    }

    @Test
    fun `material with no hash can never accept anything`() {
        val derivation = matching()
        val verifier = verifier(RecoveryMaterial(salt = SALT, iterations = ITERATIONS, hash = ""), derivation)

        assertFalse("a device with nothing to compare against reported usable material", verifier.usable())
        assertFalse(verifier.verify(CODE))
        assertEquals(0, derivation.calls)
    }

    /**
     * A truncated or corrupted field, which is what a half-written credential file looks like.
     *
     * `~` is in neither base64 alphabet, so this is a decode failure rather than a short value. The
     * standard-alphabet case is the vectors' business — they carry a hash with `-` and `_` in it,
     * which the *standard* decoder rejects, so a mirror that reached for `Base64.getDecoder()` fails
     * there rather than here.
     */
    @Test
    fun `material that is not base64 at all can never accept anything`() {
        val derivation = matching()
        val verifier = verifier(
            RecoveryMaterial(salt = "~~~not base64~~~", iterations = ITERATIONS, hash = HASH),
            derivation,
        )

        assertFalse("an undecodable salt reported usable material", verifier.usable())
        assertFalse(verifier.verify(CODE))
        assertEquals(0, derivation.calls)
    }

    /**
     * The case calibration found unbound.
     *
     * Zero rounds is not a weak hash, it is *no* hash — PBKDF2 at zero iterations is undefined, and
     * a provider that produced anything at all would produce something unrelated to what the server
     * stored. It reaches a device the same way any other field does: a credential written by a
     * server that did not fill it in, or a JSON default surviving a parse that dropped the key.
     *
     * The screen must call that unavailable rather than prompt. A prompt would reject every code the
     * parent types, on the one screen whose entire purpose is to work when nothing else does.
     */
    @Test
    fun `a zero work factor is unusable material, not a code that keeps being wrong`() {
        val derivation = matching()
        val verifier = verifier(RecoveryMaterial(salt = SALT, iterations = 0, hash = HASH), derivation)

        assertFalse("material with a zero work factor reported itself usable", verifier.usable())
        assertFalse(verifier.verify(CODE))
        assertEquals("a code was derived at zero rounds", 0, derivation.calls)
    }

    @Test
    fun `a negative work factor is unusable material too`() {
        val derivation = matching()
        val verifier = verifier(RecoveryMaterial(salt = SALT, iterations = -1, hash = HASH), derivation)

        assertFalse(verifier.usable())
        assertFalse(verifier.verify(CODE))
        assertEquals(0, derivation.calls)
    }

    // ---- material that can match -------------------------------------------------------------

    @Test
    fun `material the server issued is usable, and nothing is derived to find that out`() {
        val derivation = matching()
        val verifier = verifier(RecoveryMaterial(salt = SALT, iterations = ITERATIONS, hash = HASH), derivation)

        assertTrue("real recovery material reported itself unusable", verifier.usable())
        // usable() is called on every screen open, before the parent has typed anything. Deriving
        // there would spend 120 000 rounds of PBKDF2 answering a question about three strings.
        assertEquals("asking whether recovery is possible derived a key", 0, derivation.calls)
    }

    @Test
    fun `the code that derives to the stored hash is accepted`() {
        val derivation = matching()

        assertTrue(verifier(usable(), derivation).verify(CODE))
        assertEquals(1, derivation.calls)
    }

    /**
     * One byte apart, not a wholly different value: a comparison that stops early, or one that
     * compares prefixes, or one that compares lengths, all accept a near miss and reject a far one.
     */
    @Test
    fun `a digest that differs in its last byte is refused`() {
        val nearMiss = HASH_BYTES.copyOf().also { it[it.size - 1] = (it[it.size - 1] + 1).toByte() }

        assertFalse(verifier(usable(), FakeDerivation { _, _, _ -> nearMiss }).verify(CODE))
    }

    @Test
    fun `a digest that is a prefix of the stored hash is refused`() {
        val short = HASH_BYTES.copyOf(HASH_BYTES.size - 1)

        assertFalse(verifier(usable(), FakeDerivation { _, _, _ -> short }).verify(CODE))
    }

    /**
     * The salt and the work factor reach the derivation exactly as the server sent them.
     *
     * A verifier that decoded the salt with the wrong alphabet, or passed its own idea of the
     * iteration count, would still be *internally* consistent — and would disagree with the server
     * about every code. The vectors catch that too; this catches it with a message that names the
     * field.
     */
    @Test
    fun `the salt and work factor are passed through as the server sent them`() {
        val derivation = matching()

        verifier(usable(), derivation).verify(CODE)

        assertArrayEquals("the decoded salt", SALT_BYTES, derivation.lastSalt)
        assertEquals("the work factor", ITERATIONS, derivation.lastIterations)
    }

    /** The fold happens before the derivation, so what is hashed is the canonical form. */
    @Test
    fun `the entry is folded before it is derived from`() {
        val derivation = matching()

        verifier(usable(), derivation).verify("  $CODE_WITH_GROUPS \n")

        assertEquals(CODE, derivation.lastCode)
    }

    // ---- everything that can go wrong denies -------------------------------------------------

    /**
     * A blank entry is refused, and nothing is derived from it.
     *
     * The derivation is the assertion. Providers disagree about an empty password — the JDK's
     * refuses one outright, others hash it happily — so a device that derived from "" would either
     * throw or produce a real digest, and which one it did would depend on the phone. The guard
     * makes "typed nothing" mean the same thing everywhere.
     *
     * This is the last line of defence rather than the first: against a real code's digest, an empty
     * derivation simply does not match, so the comparison rescues a missing guard and the mistake
     * only surfaces on a device whose stored hash is itself the digest of nothing. Reaching that
     * state takes a bug upstream, which is exactly when the last line is all that is left.
     */
    @Test
    fun `an entry that folds to nothing is refused without deriving anything`() {
        // Escapes, not the characters: a line of things that all render as a blank or a hyphen
        // is a line no reviewer can check, and one of them turning into an ordinary space in an
        // editor would be invisible in the diff. U+00A0 is here because it is the one Go strips
        // and Character.isWhitespace does not.
        for (blank in listOf("", "   ", "-- --", "\u2013 \u00a0 \u2014")) {
            val derivation = matching()
            assertFalse("\"$blank\" was accepted", verifier(usable(), derivation).verify(blank))
            assertEquals("\"$blank\" was derived from", 0, derivation.calls)
        }
    }

    /**
     * A provider that throws denies. It does not propagate, and it does not unlock.
     *
     * The failure of the check is not evidence about the code. An exception escaping here would
     * reach the activity as a crash on the recovery screen — the one screen a parent gets to when
     * everything else has already gone wrong.
     */
    @Test
    fun `a derivation that throws denies rather than propagating`() {
        val exploding = FakeDerivation { _, _, _ -> throw IllegalStateException("no such provider") }

        assertFalse(verifier(usable(), exploding).verify(CODE))
        assertEquals("the derivation was not reached at all", 1, exploding.calls)
    }

    /** A provider that answers with nothing is not a provider that answered correctly. */
    @Test
    fun `a derivation that returns an empty array is refused`() {
        assertFalse(verifier(usable(), FakeDerivation { _, _, _ -> ByteArray(0) }).verify(CODE))
    }

    /**
     * The RawURL alphabet, pinned by the two characters that tell it apart from the standard one.
     *
     * `----____` is a valid encoding under `base64.RawURLEncoding` and an *error* under the standard
     * decoder, so this fails rather than mis-decodes if the alphabet is ever changed. Roughly one
     * salt in eight contains at least one of these, which is what makes the wrong decoder a bug that
     * bricks some devices and not others — the worst possible distribution for finding it in the
     * field.
     */
    @Test
    fun `the salt is decoded with the URL alphabet, dash and underscore included`() {
        val derivation = matching()
        val verifier = verifier(
            RecoveryMaterial(salt = URL_ALPHABET_SALT, iterations = ITERATIONS, hash = HASH),
            derivation,
        )

        assertTrue("a salt using the URL alphabet was read as undecodable", verifier.usable())
        verifier.verify(CODE)
        assertArrayEquals(URL_ALPHABET_SALT_BYTES, derivation.lastSalt)
    }

    @Test
    fun `an entry is never accepted on unusable material, whatever the derivation answers`() {
        // The derivation would accept anything. Nothing must get past the material check anyway.
        val alwaysMatching = FakeDerivation { _, _, _ -> HASH_BYTES }
        val unusable = listOf(
            RecoveryMaterial(salt = "", iterations = ITERATIONS, hash = HASH),
            RecoveryMaterial(salt = SALT, iterations = ITERATIONS, hash = ""),
            RecoveryMaterial(salt = SALT, iterations = 0, hash = HASH),
            RecoveryMaterial(),
        )

        for (material in unusable) {
            assertFalse("$material accepted a code", verifier(material, alwaysMatching).verify(CODE))
        }
        assertEquals(0, alwaysMatching.calls)
        assertNull("the derivation saw a code it should never have been given", alwaysMatching.lastCode)
    }

    private fun usable() = RecoveryMaterial(salt = SALT, iterations = ITERATIONS, hash = HASH)

    private companion object {
        /**
         * Fixtures, not vectors: the bytes are arbitrary and the derivation is faked, so nothing
         * here asserts anything about PBKDF2. What the real numbers look like is
         * `recovery-vectors.json`'s business.
         */
        val SALT_BYTES = ByteArray(16) { (it * 7 + 3).toByte() }
        val HASH_BYTES = ByteArray(32) { (it * 11 + 5).toByte() }

        val SALT: String = Base64.getUrlEncoder().withoutPadding().encodeToString(SALT_BYTES)
        val HASH: String = Base64.getUrlEncoder().withoutPadding().encodeToString(HASH_BYTES)

        /** Six bytes whose RawURL encoding is `----____`: four 62s followed by four 63s. */
        val URL_ALPHABET_SALT_BYTES = byteArrayOf(
            0xFB.toByte(), 0xEF.toByte(), 0xBE.toByte(),
            0xFF.toByte(), 0xFF.toByte(), 0xFF.toByte(),
        )
        const val URL_ALPHABET_SALT = "----____"

        const val ITERATIONS = 120_000

        /** Already folded — uppercase, no separators — so a passing test says the fold ran. */
        const val CODE = "23456789ABCDEFGHJKMN"
        const val CODE_WITH_GROUPS = "2345-6789-abcd-efgh-jkmn"
    }
}
