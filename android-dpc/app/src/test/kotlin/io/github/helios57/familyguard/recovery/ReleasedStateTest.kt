package io.github.helios57.familyguard.recovery

import io.github.helios57.familyguard.enforce.DesiredState
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.encodeToJsonElement
import kotlinx.serialization.json.jsonObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * What a recovered device enforces: nothing at all.
 *
 * [releasedState] is `DesiredState()`, and the reason that is safe rather than merely short is that
 * every field of [DesiredState] defaults to the value meaning *not enforced*. That is a property of
 * a class in another package, held in place by nothing but the habit of whoever adds the next field.
 *
 * So this asserts it by reflection rather than field by field. A hand-written list of assertions is
 * exactly as complete as the day it was written: a `DesiredState` gaining a `require_pin = true`
 * would leave one restriction standing on a phone the parent believes they have released, and every
 * test in this repository would stay green. Encoding with `encodeDefaults` and refusing anything
 * that is not `false`, `0`, `""` or `[]` fails on the field that does not exist yet.
 *
 * A field whose neutral value genuinely is `true` would fail here and should: it means "release the
 * device" can no longer be spelled `DesiredState()`, and the person adding it needs to say what
 * releasing means instead.
 */
class ReleasedStateTest {

    private val json = Json { encodeDefaults = true }

    @Test
    fun `every field of the released state means not enforced`() {
        val encoded = json.encodeToJsonElement(releasedState()).jsonObject

        // An empty object would pass every check below by having nothing to check. This is the
        // difference between "no field enforces anything" and "there are no fields".
        assertTrue("the released state serialised to nothing at all", encoded.size >= 10)
        assertTrue(
            "the released state has no `locked` field, so this is not the class it claims to be",
            encoded.containsKey("locked"),
        )

        for ((field, value) in encoded) {
            val neutral = when (value) {
                is JsonArray -> value.isEmpty()
                is JsonPrimitive ->
                    if (value.isString) value.content.isEmpty() else value.content in NEUTRAL_SCALARS
                else -> false
            }
            assertTrue(
                "the released state enforces `$field` = $value. Recovery must leave nothing " +
                    "standing, so either this field's neutral value is not its default, or " +
                    "releasedState() can no longer be DesiredState().",
                neutral,
            )
        }
    }

    /**
     * The released state is the *default* state, not a state that happens to look like it.
     *
     * Pinned because the cheap way to satisfy the test above is to enumerate the fields by hand in
     * `releasedState()`, which reintroduces exactly the list that goes stale.
     */
    @Test
    fun `the released state is the default state`() {
        assertEquals(DesiredState(), releasedState())
    }

    private companion object {
        /** `false` and `0`: the two non-string neutrals. Anything else is an opinion. */
        val NEUTRAL_SCALARS = setOf("false", "0")
    }
}
