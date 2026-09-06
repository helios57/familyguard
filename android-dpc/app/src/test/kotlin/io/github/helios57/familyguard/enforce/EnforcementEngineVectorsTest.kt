package io.github.helios57.familyguard.enforce

import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.encodeToJsonElement
import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * Replays `backend/internal/policy/vectors.json` — the same file, the same cases and the same
 * expected output as the Go engine's `TestSharedVectors`.
 *
 * This is the only control that can catch the two enforcement engines drifting apart. Drift does not
 * crash anything: it is a phone that ends bedtime at 06:00 while the console says 07:00, with every
 * other test on both sides green.
 */
class EnforcementEngineVectorsTest {

    /**
     * Rejects unknown keys. A misspelled input key would otherwise be dropped in silence and the
     * vector would then assert against a default value rather than the one someone wrote down — a
     * green test measuring something nobody asked for.
     */
    private val strict = Json { ignoreUnknownKeys = false }

    /** encodeDefaults, so every field of the result is present and an expectation naming one cannot miss. */
    private val emitting = Json { encodeDefaults = true }

    private fun loadVectors(): List<JsonObject> {
        // Copied onto the test classpath by the copyPolicyVectors task, which fails the build when
        // the source has moved. A test that read `../../../backend/...` instead would survive a
        // refactor by finding nothing and asserting over zero vectors.
        val raw = javaClass.classLoader?.getResourceAsStream("vectors.json")?.use {
            it.readBytes().decodeToString()
        }
        if (raw.isNullOrEmpty()) {
            throw AssertionError(
                "vectors.json is not on the test classpath. It is copied there from " +
                    "backend/internal/policy/ by the copyPolicyVectors task; without it this suite " +
                    "asserts nothing and must not be read as a pass.",
            )
        }
        val list = strict.parseToJsonElement(raw).jsonObject["vectors"]?.jsonArray
            ?: throw AssertionError("shared vector file has no \"vectors\" key")
        assertTrue("the shared vector file contains no vectors", list.isNotEmpty())
        return list.map { it.jsonObject }
    }

    @Test
    fun `every shared vector produces the same desired state as the Go engine`() {
        val vectors = loadVectors()
        val seen = mutableSetOf<String>()
        var comparisons = 0

        for ((i, v) in vectors.withIndex()) {
            val name = v["name"]?.jsonPrimitive?.content
                ?: throw AssertionError("vector $i has no name")
            assertTrue(
                "duplicate vector name \"$name\" — a rename would silently drop a case",
                seen.add(name),
            )

            val input = strict.decodeFromJsonElement(Input.serializer(), v.require(name, "input"))
            val expect = v.require(name, "expect").jsonObject
            assertTrue("vector \"$name\" asserts nothing", expect.isNotEmpty())

            val actual = emitting.encodeToJsonElement(EnforcementEngine.compute(input)).jsonObject

            for ((key, want) in expect) {
                val have = actual[key] ?: throw AssertionError(
                    "vector \"$name\" expects \"$key\", which is not a field of DesiredState",
                )
                assertEquals("vector \"$name\", field $key", want, have)
                comparisons++
            }
        }

        // The loop above is vacuously green over an empty file, and so is a suite whose resource
        // arrived truncated. Pin what was actually compared, so "0 of 0 vectors agreed" cannot read
        // as agreement. Both numbers are lower-bounded rather than exact where growth is expected.
        assertEquals("number of vectors replayed", 26, vectors.size)
        assertTrue("only $comparisons expectations were compared", comparisons >= 100)
    }

    private fun JsonObject.require(vectorName: String, key: String): JsonElement =
        this[key] ?: throw AssertionError("vector \"$vectorName\" has no \"$key\"")
}
