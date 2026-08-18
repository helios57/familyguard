package io.github.helios57.familyguard.recovery

import io.github.helios57.familyguard.net.RecoveryMaterial
import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.decodeFromJsonElement
import kotlinx.serialization.json.jsonObject
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import java.util.Base64

/**
 * Replays `backend/internal/auth/recovery-vectors.json` — the same file, the same cases and the same
 * expectations as the Go side's `TestRecoveryVectors`.
 *
 * This is the only control that can catch the two implementations of the recovery code drifting
 * apart, and drift here is silent on both sides: every test in this repository stays green while the
 * phone rejects the twenty characters printed on the parent's screen. By then the device is locked
 * down, possibly offline, and there is no way to read anything back off it.
 *
 * The file is not generated from either side. Its normalisation expectations are hand-written from
 * the rule in `tokens.go`, and its digests come from Python's `hashlib` — a third implementation of
 * RFC 8018. Vectors taken from Go's output would assert that Go does what Go does; taken from this
 * side's, that Kotlin does what Kotlin does. Neither is evidence that the two agree.
 */
class RecoveryVectorsTest {

    @Serializable
    private data class NormalizeVector(
        @SerialName("name") val name: String,
        @SerialName("raw") val raw: String,
        @SerialName("expect") val expect: String,
    )

    @Serializable
    private data class DeriveVector(
        @SerialName("name") val name: String,
        @SerialName("code") val code: String,
        @SerialName("salt") val salt: String,
        @SerialName("iterations") val iterations: Int,
        @SerialName("expect_hash") val expectHash: String,
    )

    @Serializable
    private data class VerifyVector(
        @SerialName("name") val name: String,
        @SerialName("code") val code: String,
        @SerialName("salt") val salt: String,
        @SerialName("iterations") val iterations: Int,
        @SerialName("hash") val hash: String,
        @SerialName("expect") val expect: Boolean,
    )

    /**
     * Rejects unknown keys, for the reason the Go loader states: a misspelled key would otherwise be
     * dropped in silence and the vector would assert against a default. An `iterations` typo would
     * run every derive case at zero rounds, which fails — and reads as an implementation correctly
     * refusing a zero work factor rather than as a broken vector file.
     */
    private val strict = Json { ignoreUnknownKeys = false }

    private inline fun <reified T> section(name: String): List<T> {
        // Copied onto the test classpath by copyRecoveryVectors, which fails the build if the source
        // has moved. A test reading `../../../backend/...` would survive that refactor by finding
        // nothing and asserting over zero vectors.
        val raw = javaClass.classLoader?.getResourceAsStream("recovery-vectors.json")?.use {
            it.readBytes().decodeToString()
        }
        if (raw.isNullOrEmpty()) {
            throw AssertionError(
                "recovery-vectors.json is not on the test classpath. It is copied there from " +
                    "backend/internal/auth/ by the copyRecoveryVectors task; without it this suite " +
                    "asserts nothing and must not be read as a pass.",
            )
        }
        val array = strict.parseToJsonElement(raw).jsonObject[name] as? JsonArray
            ?: throw AssertionError("the shared recovery vectors have no \"$name\" section")
        assertTrue("the \"$name\" section is empty, so replaying it asserts nothing", array.isNotEmpty())
        return array.map { strict.decodeFromJsonElement<T>(it) }
    }

    /** RawURL, matching `base64.RawURLEncoding` on the server: the decoder tolerates the absent padding. */
    private fun decode(value: String): ByteArray =
        if (value.isEmpty()) ByteArray(0) else Base64.getUrlDecoder().decode(value)

    @Test
    fun `every normalize vector folds the way the server folds it`() {
        val seen = mutableSetOf<String>()
        for (v in section<NormalizeVector>("normalize")) {
            assertTrue("duplicate normalize vector \"${v.name}\"", seen.add(v.name))
            assertEquals(
                "vector \"${v.name}\": normalize(${v.raw.escaped()})",
                v.expect,
                RecoveryCode.normalize(v.raw),
            )
        }
        requirePresent(seen, DIVERGENCE_VECTORS, "normalize")
    }

    @Test
    fun `every derive vector produces the digest a third implementation produced`() {
        val seen = mutableSetOf<String>()
        for (v in section<DeriveVector>("derive")) {
            assertTrue("duplicate derive vector \"${v.name}\"", seen.add(v.name))
            val want = decode(v.expectHash)
            assertEquals("vector \"${v.name}\": digest length", KEY_BYTES, want.size)

            // normalize-then-derive, which is what DeriveRecoveryHash does on the other side in one
            // call. Split here because RecoveryDerivation takes the folded code: the verifier has to
            // be testable without a JCE provider, so the fold happens outside it.
            val got = Pbkdf2HmacSha256.derive(RecoveryCode.normalize(v.code), decode(v.salt), v.iterations)
            assertArrayEquals("vector \"${v.name}\": digest", want, got)
        }
        assertTrue(
            "no derive vector runs at the production work factor, so nothing here checks the number " +
                "a device actually runs",
            seen.contains(PRODUCTION_WORK_FACTOR_VECTOR),
        )
    }

    @Test
    fun `every verify vector is accepted or refused the way the server decides`() {
        val seen = mutableSetOf<String>()
        var accept = 0
        var reject = 0
        for (v in section<VerifyVector>("verify")) {
            assertTrue("duplicate verify vector \"${v.name}\"", seen.add(v.name))
            if (v.expect) accept++ else reject++

            val verifier = RecoveryVerifier(
                RecoveryMaterial(salt = v.salt, iterations = v.iterations, hash = v.hash),
            )
            assertEquals("vector \"${v.name}\"", v.expect, verifier.verify(v.code))
        }

        // A section of only accepting cases is passed by a verifier that returns true, and one of
        // only rejecting cases by a verifier that returns false. Both are what a half-finished
        // mirror looks like.
        assertTrue(
            "verify vectors: $accept accepting, $reject rejecting — a section that is all one " +
                "answer is passed by a constant",
            accept > 0 && reject > 0,
        )
    }

    /**
     * The vectors that exist *because* the two platforms differ, pinned by name.
     *
     * Not a count. The failure this guards against is not a truncated file — that fails to parse —
     * but the plausible, well-meant edit: a mirror fails one of these, and the vector is deleted or
     * softened to make the build green. A count would be updated in the same breath. A missing name
     * says which specific disagreement stopped being checked.
     */
    private fun requirePresent(seen: Set<String>, required: List<String>, section: String) {
        val missing = required.filterNot(seen::contains)
        assertTrue(
            "the $section section no longer contains ${missing.joinToString(", ") { "\"$it\"" }}. " +
                "Each of these pins a known Go/Java divergence, so removing one does not make the " +
                "two sides agree — it stops measuring whether they do.",
            missing.isEmpty(),
        )
    }

    /** Renders control characters and blanks visibly, so a failure message can be read. */
    private fun String.escaped(): String =
        "\"" + map { c ->
            when {
                c == '"' -> "\\\""
                c == '\\' -> "\\\\"
                c.code in 0x20..0x7E -> c.toString()
                else -> "\\u%04x".format(c.code)
            }
        }.joinToString("") + "\""

    private companion object {
        const val KEY_BYTES = 32

        /**
         * U+0085, U+00A0, U+2007 and U+202F are whitespace to Go and not to `Character.isWhitespace`;
         * U+001C..U+001F are the reverse; and `String.uppercase()` turns ß into SS where Go's simple
         * mapping leaves it. Each of these folds differently on the two sides unless the mirror is
         * written deliberately — which is the whole reason this suite exists.
         *
         * Those four are the *complete* Go-minus-Java set, so a mirror that passes this list has no
         * remaining whitespace disagreement to find in production.
         */
        val DIVERGENCE_VECTORS = listOf(
            "a non-breaking space at each end and in the middle, as a chat app pastes it",
            "the four spaces Go strips that Java does not call whitespace at all",
            "an ASCII file separator survives, because Go does not call it a space",
            "a sharp s stays one character, because both sides map case simply",
        )

        const val PRODUCTION_WORK_FACTOR_VECTOR =
            "the production work factor, so the number that ships is the number that is checked"
    }
}
