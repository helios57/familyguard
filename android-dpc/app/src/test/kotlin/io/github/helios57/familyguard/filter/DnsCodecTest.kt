package io.github.helios57.familyguard.filter

import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test
import java.io.ByteArrayOutputStream

/**
 * Reading a DNS question, and refusing everything that is not one.
 *
 * Half of these are refusals, and that is the point: this parser's inputs are chosen by whatever
 * app is running, so the cases that matter are the ones a well-behaved resolver never produces.
 */
class DnsCodecTest {

    @Test
    fun `a query is read back as the name the filter looks up`() {
        val query = DnsCodec.parseQuery(dnsQuery("ADS.Example.COM"))!!

        assertEquals("lowercased, no trailing dot", "ads.example.com", query.name)
        assertEquals(DnsCodec.TYPE_A, query.type)
        assertEquals(DnsCodec.CLASS_IN, query.recordClass)
        assertEquals(0x1234, query.transactionId)
    }

    /** Chrome asks for HTTPS (65) before A, so a filter blind to it leaks the whole connection. */
    @Test
    fun `the record type is carried, not assumed to be A`() {
        assertEquals(
            DnsCodec.TYPE_HTTPS,
            DnsCodec.parseQuery(dnsQuery("ads.example.com", type = DnsCodec.TYPE_HTTPS))!!.type,
        )
        assertEquals(
            DnsCodec.TYPE_AAAA,
            DnsCodec.parseQuery(dnsQuery("ads.example.com", type = DnsCodec.TYPE_AAAA))!!.type,
        )
    }

    @Test
    fun `the answer echoes the id and the question and says name error`() {
        val packet = dnsQuery("ads.example.com")
        val query = DnsCodec.parseQuery(packet)!!

        val answer = DnsCodec.nxDomainFor(packet, 0, query)

        assertEquals("id echoed", 0x1234, u16(answer, 0))
        assertEquals("QR set", 0x8000, u16(answer, 2) and 0x8000)
        assertEquals("RD copied from the query", 0x0100, u16(answer, 2) and 0x0100)
        assertEquals("RA set, so the client does not retry the same resolver", 0x0080, u16(answer, 2) and 0x0080)
        assertEquals("RCODE 3, NXDOMAIN", 3, u16(answer, 2) and 0x000F)
        assertEquals(1, u16(answer, 4))
        assertEquals("no answer, no authority, no additional", 0, u16(answer, 6) + u16(answer, 8) + u16(answer, 10))
        assertArrayEquals(
            "the question is returned verbatim",
            packet.copyOfRange(12, query.questionEnd),
            answer.copyOfRange(12, answer.size),
        )
    }

    /** The response is what comes back from the resolver; answering it again would be a loop. */
    @Test
    fun `a response is not a query`() {
        assertNull(DnsCodec.parseQuery(dnsQuery("ads.example.com", flags = 0x8180)))
    }

    @Test
    fun `an update or a notify is not a name lookup`() {
        // OPCODE 5 (UPDATE) in bits 14..11.
        assertNull(DnsCodec.parseQuery(dnsQuery("ads.example.com", flags = 0x2800)))
    }

    /**
     * NXDOMAIN is per-message, so a two-question query cannot be answered one question at a time.
     * Refusing it means the packet is forwarded untouched, which is the only honest outcome.
     */
    @Test
    fun `a query with more than one question is refused rather than half-answered`() {
        assertNull(DnsCodec.parseQuery(dnsQuery("ads.example.com", questions = 2)))
        assertNull(DnsCodec.parseQuery(dnsQuery("ads.example.com", questions = 0)))
    }

    /** There is nothing behind the question to point at, and following one is how a parser loops. */
    @Test
    fun `a compression pointer in the question is refused, not followed`() {
        val out = ByteArrayOutputStream()
        out.write(header(id = 0x1234, flags = 0x0100, questions = 1))
        out.write(byteArrayOf(0xC0.toByte(), 0x0C)) // pointer back to the header
        out.write(byteArrayOf(0, 1, 0, 1))

        assertNull(DnsCodec.parseQuery(out.toByteArray()))
    }

    /**
     * Every truncation of a real query, one byte at a time. This is the case that decides whether
     * the filter survives its first malformed packet, and it cannot be argued from the code.
     */
    @Test
    fun `no prefix of a real query throws, and none of them parses`() {
        val packet = dnsQuery("ads.example.com")
        for (length in 0 until packet.size) {
            assertNull("a $length-byte prefix must not parse", DnsCodec.parseQuery(packet, 0, length))
        }
        // The whole thing still does, so the loop above was not vacuous.
        assertEquals("ads.example.com", DnsCodec.parseQuery(packet)!!.name)
    }

    @Test
    fun `a label that runs past the end of the packet is refused`() {
        val out = ByteArrayOutputStream()
        out.write(header(id = 1, flags = 0x0100, questions = 1))
        out.write(byteArrayOf(40)) // claims 40 bytes of label
        out.write("ads".toByteArray())

        assertNull(DnsCodec.parseQuery(out.toByteArray()))
    }

    @Test
    fun `a name that is not printable ASCII is refused`() {
        val out = ByteArrayOutputStream()
        out.write(header(id = 1, flags = 0x0100, questions = 1))
        out.write(byteArrayOf(3, 0x61, 0x00, 0x62)) // a NUL inside the label
        out.write(byteArrayOf(0, 0, 1, 0, 1))

        assertNull(DnsCodec.parseQuery(out.toByteArray()))
    }

    @Test
    fun `a query for the root is refused, because there is no name in it`() {
        val out = ByteArrayOutputStream()
        out.write(header(id = 1, flags = 0x0100, questions = 1))
        out.write(byteArrayOf(0, 0, 1, 0, 1))

        assertNull(DnsCodec.parseQuery(out.toByteArray()))
    }

    /** The tunnel hands over a buffer with the packet somewhere inside it, never at zero. */
    @Test
    fun `the question is read at an offset inside a larger buffer`() {
        val packet = dnsQuery("ads.example.com")
        val buffer = ByteArray(64) { 0x7F } + packet + ByteArray(32) { 0x7F }

        val query = DnsCodec.parseQuery(buffer, 64, packet.size)!!

        assertEquals("ads.example.com", query.name)
        assertEquals(0x1234, u16(DnsCodec.nxDomainFor(buffer, 64, query), 0))
    }

    // ---- fixtures ---------------------------------------------------------------------------

    private fun dnsQuery(
        name: String,
        type: Int = DnsCodec.TYPE_A,
        id: Int = 0x1234,
        flags: Int = 0x0100,
        questions: Int = 1,
    ): ByteArray {
        val out = ByteArrayOutputStream()
        out.write(header(id, flags, questions))
        repeat(maxOf(questions, 1)) {
            for (label in name.split('.')) {
                out.write(label.length)
                out.write(label.toByteArray(Charsets.US_ASCII))
            }
            out.write(0)
            out.write(byteArrayOf((type shr 8).toByte(), type.toByte(), 0, 1))
        }
        return out.toByteArray()
    }

    private fun header(id: Int, flags: Int, questions: Int) = byteArrayOf(
        (id shr 8).toByte(), id.toByte(),
        (flags shr 8).toByte(), flags.toByte(),
        (questions shr 8).toByte(), questions.toByte(),
        0, 0, 0, 0, 0, 0,
    )

    private fun u16(b: ByteArray, at: Int) = ((b[at].toInt() and 0xFF) shl 8) or (b[at + 1].toInt() and 0xFF)
}
