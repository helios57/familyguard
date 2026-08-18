package io.github.helios57.familyguard.net

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

class SseParserTest {

    private fun feed(parser: SseParser, vararg lines: String): List<SseEvent> =
        lines.mapNotNull { parser.line(it) }

    @Test
    fun `a complete frame is emitted on the blank line that ends it`() {
        val parser = SseParser()
        assertNull(parser.line("event: policy"))
        assertNull(parser.line("""data: {"type":"policy"}"""))
        assertEquals(SseEvent("policy", """{"type":"policy"}"""), parser.line(""))
    }

    @Test
    fun `a keepalive comment is not an event`() {
        // The server writes one of these every 20 seconds. If it parses as an event, an idle phone
        // wakes a full policy sync three times a minute forever.
        val parser = SseParser()
        assertEquals(emptyList<SseEvent>(), feed(parser, ":", ": keepalive", ""))
    }

    @Test
    fun `a keepalive between the fields of a frame does not break it`() {
        val parser = SseParser()
        assertEquals(
            listOf(SseEvent("command", "x")),
            feed(parser, "event: command", ": ping", "data: x", ""),
        )
    }

    @Test
    fun `exactly one leading space is stripped, not all whitespace`() {
        val parser = SseParser()
        assertEquals(listOf(SseEvent("message", "  padded")), feed(parser, "data:   padded", ""))
    }

    @Test
    fun `a value with no space after the colon keeps every character`() {
        val parser = SseParser()
        assertEquals(listOf(SseEvent("message", "tight")), feed(parser, "data:tight", ""))
    }

    @Test
    fun `multiple data lines are joined with a newline`() {
        val parser = SseParser()
        assertEquals(listOf(SseEvent("message", "one\ntwo")), feed(parser, "data: one", "data: two", ""))
    }

    @Test
    fun `a frame with no event field defaults to message`() {
        val parser = SseParser()
        assertEquals(listOf(SseEvent("message", "x")), feed(parser, "data: x", ""))
    }

    @Test
    fun `a data field with no colon is an empty data line`() {
        val parser = SseParser()
        assertEquals(listOf(SseEvent("message", "")), feed(parser, "data", ""))
    }

    @Test
    fun `unknown fields are ignored and do not by themselves make a frame`() {
        // A server that starts stamping its keepalives with an id must not turn every one of them
        // into a wake-up on every deployed phone.
        val parser = SseParser()
        assertEquals(emptyList<SseEvent>(), feed(parser, "id: 7", "retry: 5000", ""))
    }

    @Test
    fun `consecutive blank lines emit nothing`() {
        val parser = SseParser()
        assertEquals(emptyList<SseEvent>(), feed(parser, "", "", ""))
    }

    @Test
    fun `a CRLF terminated stream parses identically`() {
        val parser = SseParser()
        assertEquals(
            listOf(SseEvent("policy", "x")),
            feed(parser, "event: policy\r", "data: x\r", "\r"),
        )
    }

    @Test
    fun `two frames in a row do not bleed into each other`() {
        val parser = SseParser()
        assertEquals(
            listOf(SseEvent("connected", "a"), SseEvent("policy", "b")),
            feed(parser, "event: connected", "data: a", "", "event: policy", "data: b", ""),
        )
    }

    @Test
    fun `reset drops a half read frame`() {
        val parser = SseParser()
        parser.line("event: policy")
        parser.line("data: half")
        parser.reset()
        assertEquals(listOf(SseEvent("message", "whole")), feed(parser, "data: whole", ""))
    }
}
