package io.github.helios57.familyguard.net

/** One server-sent event: its type and its data, with the field syntax already removed. */
data class SseEvent(val type: String, val data: String)

/**
 * An incremental parser for the `text/event-stream` grammar, fed one line at a time.
 *
 * It is a separate, Android-free class because every interesting case here is a case a running
 * server will not produce on demand: a `data:` field split across two lines, a keepalive comment
 * arriving mid-event, a frame that never terminates. Testing those against a live stream means
 * hoping they occur; testing them here means writing them down.
 *
 * The rules implemented are the ones the specification actually states, and three of them are the
 * kind that look like details until a stream stops working:
 *
 * - **A line beginning with `:` is a comment.** That is what a keepalive is — the server writes
 *   `:` every 20s to hold the connection open through a NAT. A parser that treats it as data emits
 *   an empty event every 20s, and a receiver that wakes a sync per event then syncs three times a
 *   minute forever, on an idle phone, on battery. Here it falls out of the unknown-field rule
 *   rather than having a branch of its own; see the note at the parse site.
 * - **Exactly one leading space is stripped after the colon**, not all whitespace. `data:  {` has a
 *   payload beginning with a space; JSON tolerates that, and a parser that strips greedily would
 *   silently corrupt any field where it does not.
 * - **Multiple `data:` lines are joined with a newline**, not concatenated. Nothing this server
 *   sends is multi-line today, which is precisely why the rule has to be written down now rather
 *   than discovered later by a payload that grows one.
 *
 * A field with no colon at all is a field name with an empty value (`data` alone means an empty
 * data line); unknown field names are ignored, which is what lets `id:` and `retry:` be added
 * server-side without breaking every deployed phone.
 */
class SseParser {

    private var type: String? = null
    private val data = StringBuilder()
    private var sawField = false

    /**
     * Feeds one line, with its terminator already removed.
     *
     * Returns the completed event when this line was the blank one that ends a frame, and null
     * otherwise. A blank line with nothing accumulated returns null rather than an empty event:
     * consecutive blank lines are a legal way to keep a connection warm.
     */
    fun line(raw: String): SseEvent? {
        // A stream may arrive CRLF-terminated; readLine() on the wire strips only the LF.
        val line = raw.removeSuffix("\r")

        if (line.isEmpty()) {
            if (!sawField) return null
            val event = SseEvent(type ?: DEFAULT_TYPE, data.toString())
            reset()
            return event
        }

        // A comment line needs no case of its own, and it used to have one. `:` at position 0 makes
        // the field name empty, and an empty name is not `event` or `data`, so the unknown-field
        // rule below already ignores it without marking the frame as having content. The explicit
        // `startsWith(":")` branch that stood here was measured: deleting it left every keepalive
        // test green, because it was a second mechanism for a property the first one already had.
        // Two mechanisms for one property means one of them is never exercised, and neither the
        // tests nor the next reader can tell which.
        val colon = line.indexOf(':')
        val field: String
        val value: String
        if (colon < 0) {
            field = line
            value = ""
        } else {
            field = line.substring(0, colon)
            val rest = line.substring(colon + 1)
            value = if (rest.startsWith(" ")) rest.substring(1) else rest
        }

        when (field) {
            "event" -> {
                type = value
                sawField = true
            }

            "data" -> {
                if (data.isNotEmpty()) data.append('\n')
                data.append(value)
                sawField = true
            }
            // id, retry and anything else: accepted and ignored. They must not mark the frame as
            // having content, or a server adding `id:` to its keepalives would make every one of
            // them emit an event.
            else -> Unit
        }
        return null
    }

    /** Drops any half-read frame. Called when a connection is torn down mid-event. */
    fun reset() {
        type = null
        data.setLength(0)
        sawField = false
    }

    private companion object {
        const val DEFAULT_TYPE = "message"
    }
}
