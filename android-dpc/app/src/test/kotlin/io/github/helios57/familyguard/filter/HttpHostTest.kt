package io.github.helios57.familyguard.filter

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * The `Host:` header, and the three ways reading one goes wrong.
 *
 * Plain HTTP is a small share of a phone's traffic and a large share of its *ad* traffic — tracking
 * pixels and older SDKs still fire cleartext — so this is cheap coverage of exactly the hosts a
 * filter list names.
 *
 * The distinction these tests protect is between "no name here" and "no name **yet**". Folding them
 * together is the defect [ClientHello.Incomplete] exists to prevent, one protocol over: decide on a
 * prefix and the flow is allowed with nothing recording that a name was ever coming.
 */
class HttpHostTest {

    @Test
    fun `the host is read out of an ordinary request`() {
        assertEquals("ads.example.com", parse("GET /px.gif HTTP/1.1\r\nHost: ads.example.com\r\n\r\n"))
    }

    @Test
    fun `the header name is matched without regard to case, as the RFC requires`() {
        assertEquals("ads.example.com", parse("GET / HTTP/1.1\r\nhOsT:  ADS.Example.com \r\n\r\n"))
    }

    @Test
    fun `a port is dropped, because a rule never names one`() {
        assertEquals("ads.example.com", parse("GET / HTTP/1.1\r\nHost: ads.example.com:8080\r\n\r\n"))
    }

    /**
     * The header block is only complete at the blank line, and a `Host:` seen before it can still
     * be followed by another one. Answering early is answering about a request that has not
     * finished being made.
     */
    @Test
    fun `a request whose headers have not finished arriving is refused, and is still HTTP`() {
        val partial = "GET / HTTP/1.1\r\nHost: ads.example.com\r\n"

        assertNull("a header block with no blank line is not finished", parse(partial))
        assertTrue("and refusing it must mean keep buffering, not give up", mightBe(partial))
    }

    @Test
    fun `a TLS record is not HTTP and must not be waited on`() {
        // 0x16 is a TLS content type; no HTTP method starts with a byte outside A-Z.
        val record = byteArrayOf(0x16, 0x03, 0x01, 0x00, 0x2F)

        assertFalse(HttpHost.mightBeHttp(record))
        assertNull(HttpHost.parse(record))
    }

    @Test
    fun `a complete request with no Host header is answered, not waited on`() {
        assertNull(parse("GET / HTTP/1.0\r\nAccept: */*\r\n\r\n"))
    }

    /**
     * A v6 literal is bracketed and a name never is, so the brackets have to come off before
     * anything looks at it — otherwise they reach a rule lookup and a parent-visible log.
     * [RuleParser.normalise] then refuses it for having no dot, which is the wanted answer:
     * a single-label value is not something a suffix rule may match.
     *
     * A v4 literal survives, and that is not an oversight — the lists do carry address rules, and
     * an address that matches one should be blocked like any other name.
     */
    @Test
    fun `a bracketed v6 literal is unwrapped and then refused, while a v4 literal stands`() {
        assertNull(parse("GET / HTTP/1.1\r\nHost: [2001:db8::1]:443\r\n\r\n"))
        assertEquals("198.51.100.7", parse("GET / HTTP/1.1\r\nHost: 198.51.100.7\r\n\r\n"))
    }

    /** The first `Host:` wins, and a later header line cannot smuggle a second name past it. */
    @Test
    fun `a second Host header does not displace the first`() {
        val smuggled = "GET / HTTP/1.1\r\nHost: ads.example.com\r\nHost: cdn.example.org\r\n\r\n"

        assertEquals("ads.example.com", parse(smuggled))
    }

    /** Past this much there is no request coming, only something pretending to be one. */
    @Test
    fun `a header block larger than any real request is refused both ways`() {
        val huge = "GET / HTTP/1.1\r\nHost: ads.example.com\r\nX: " +
            "a".repeat(HttpHost.MAX_HEADER_BYTES) + "\r\n\r\n"

        assertNull(parse(huge))
        assertFalse("waiting past the cap is waiting forever", mightBe(huge))
    }

    @Test
    fun `an empty buffer might still become a request`() {
        assertTrue(HttpHost.mightBeHttp(ByteArray(0)))
    }

    private fun parse(text: String): String? = HttpHost.parse(text.toByteArray(Charsets.ISO_8859_1))

    private fun mightBe(text: String): Boolean = HttpHost.mightBeHttp(text.toByteArray(Charsets.ISO_8859_1))
}
