package io.github.helios57.familyguard.net

import java.io.ByteArrayInputStream
import java.io.IOException
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertThrows
import org.junit.Assert.assertTrue
import org.junit.Test

class HttpUpgradeTest {

    /**
     * The property the class exists for. adbd speaks the moment it is connected, so its first bytes
     * can share a packet with the server's 101. Everything after the blank line must still be
     * there to read, byte for byte, after the head has been parsed.
     */
    @Test
    fun `bytes that arrive with the 101 are left in the stream`() {
        val session = byteArrayOf(0x43, 0x4e, 0x58, 0x4e, 0x00, 0x0d, 0x0a, 0x0d, 0x0a, 0x7f)
        val wire = "HTTP/1.1 101 Switching Protocols\r\nUpgrade: familyguard-debug\r\nConnection: Upgrade\r\n\r\n"
            .toByteArray(Charsets.ISO_8859_1) + session
        val input = ByteArrayInputStream(wire)

        val head = HttpUpgrade.readHead(input)

        assertEquals(101, head.status)
        assertEquals("familyguard-debug", head.headers["upgrade"])
        assertArrayEquals(
            "the session's first bytes were consumed with the HTTP head",
            session,
            input.readBytes(),
        )
    }

    @Test
    fun `a refusal is reported in the server's own words`() {
        val body = """{"error":"not_found","message":"no debug stream is waiting under that id","request_id":"r"}"""
        val wire = "HTTP/1.1 404 Not Found\r\nContent-Type: application/json\r\nContent-Length: ${body.length}\r\n\r\n$body"
        val input = ByteArrayInputStream(wire.toByteArray(Charsets.UTF_8))

        val head = HttpUpgrade.readHead(input)
        val sentence = HttpUpgrade.refusal(head, input)

        assertEquals(404, head.status)
        assertTrue(sentence, sentence.contains("no debug stream is waiting under that id"))
        assertTrue(sentence, sentence.contains("404"))
    }

    @Test
    fun `a connection that closes before the blank line is an error, not an answer`() {
        val input = ByteArrayInputStream("HTTP/1.1 101 Switching Protocols\r\nUpgrade: x\r\n".toByteArray())
        assertThrows(IOException::class.java) { HttpUpgrade.readHead(input) }
    }

    @Test
    fun `something that is not HTTP is refused`() {
        val input = ByteArrayInputStream("SSH-2.0-OpenSSH\r\n\r\n".toByteArray())
        assertThrows(IOException::class.java) { HttpUpgrade.readHead(input) }
    }

    @Test
    fun `the device leg goes to the enrolled server as this device`() {
        val api = ApiClient("https://guard.example.com/", token = { "device-token" })
        val leg = api.debugLeg("0123456789abcdef0123456789abcdef")
        val request = String(leg.request, Charsets.ISO_8859_1)

        assertEquals("guard.example.com", leg.host)
        assertEquals(443, leg.port)
        assertTrue(leg.tls)
        assertTrue(request, request.startsWith("GET /api/v1/device/debug/0123456789abcdef0123456789abcdef HTTP/1.1\r\n"))
        assertTrue(request, request.contains("\r\nHost: guard.example.com\r\n"))
        assertTrue(request, request.contains("\r\nAuthorization: Bearer device-token\r\n"))
        assertTrue(request, request.contains("\r\nUpgrade: familyguard-debug\r\n"))
        assertTrue(request, request.endsWith("\r\n\r\n"))
    }

    @Test
    fun `a stream id from the server cannot write headers into the request`() {
        val api = ApiClient("https://guard.example.com", token = { "device-token" })
        assertThrows(IllegalArgumentException::class.java) {
            api.debugLeg("0123\r\nX-Evil: 1\r\n")
        }
        assertThrows(IllegalArgumentException::class.java) { api.debugLeg("") }
    }
}
