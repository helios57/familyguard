package io.github.helios57.familyguard.filter

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.ByteArrayOutputStream

/**
 * Reading the SNI, and — more importantly — knowing when there is not enough of it yet.
 *
 * `no prefix of a hello is ever mistaken for a finished one` is the test this file exists for.
 * Every other case here describes a hello that arrived whole, which is the easy half; the failure
 * that actually leaks ads is a handshake split across two segments being answered as *"complete,
 * no name"* and the flow then decided without one. Nothing about that failure is visible afterwards.
 */
class TlsClientHelloTest {

    @Test
    fun `the name is read out of a whole hello`() {
        val result = TlsClientHello.parse(clientHello(serverName = "ADS.Example.com"))

        assertEquals(ClientHello.Found("ads.example.com", echPresent = false), result)
    }

    /**
     * A ClientHello is one message, not one record, and a post-quantum key share routinely pushes
     * it past a single segment. Reassembly is the difference between filtering modern TLS and not.
     */
    @Test
    fun `a handshake split across records still yields the name`() {
        val whole = clientHello(serverName = "ads.example.com")
        val split = clientHello(serverName = "ads.example.com", recordSize = 40)

        assertTrue("the fixture really did split", split.size > whole.size)
        assertEquals(ClientHello.Found("ads.example.com", echPresent = false), TlsClientHello.parse(split))
    }

    /**
     * Every truncation, one byte at a time. A prefix may only ever be [ClientHello.Incomplete] —
     * never [ClientHello.NoName], which would make the flow machine decide with no name, and never
     * a [ClientHello.Found] carrying a name assembled out of bytes that had not all arrived.
     */
    @Test
    fun `no prefix of a hello is ever mistaken for a finished one`() {
        for (hello in listOf(clientHello(), clientHello(recordSize = 40))) {
            for (length in 1 until hello.size) {
                assertEquals(
                    "a $length-byte prefix of a ${hello.size}-byte hello",
                    ClientHello.Incomplete,
                    TlsClientHello.parse(hello, 0, length),
                )
            }
            // The whole thing parses, so the loop above was not vacuous.
            assertEquals(ClientHello.Found("ads.example.com", false), TlsClientHello.parse(hello))
        }
    }

    @Test
    fun `a hello with no server name extension names nothing`() {
        assertEquals(ClientHello.NoName, TlsClientHello.parse(clientHello(serverName = null)))
    }

    /** TLS 1.2 and earlier allow a hello that simply ends after the compression methods. */
    @Test
    fun `a hello with no extensions block at all names nothing`() {
        assertEquals(
            ClientHello.NoName,
            TlsClientHello.parse(clientHello(serverName = null, omitExtensionsBlock = true)),
        )
    }

    @Test
    fun `plain HTTP is not TLS`() {
        val get = "GET / HTTP/1.1 Host: ads.example.com".toByteArray(Charsets.US_ASCII)

        assertEquals(ClientHello.NotTls, TlsClientHello.parse(get))
    }

    /** A server's answer starts with 0x16 too. Only the handshake type tells them apart. */
    @Test
    fun `a server hello is not a client hello`() {
        val serverHello = clientHello().copyOf()
        serverHello[5] = 0x02 // handshake type ServerHello

        assertEquals(ClientHello.NotTls, TlsClientHello.parse(serverHello))
    }

    /**
     * With ECH the visible name is the provider's public name, not the host the app asked for.
     * Reported rather than swallowed: a filter that calls a decoy the truth is wrong about every
     * connection behind that provider and never says so.
     */
    @Test
    fun `an encrypted client hello is reported as the decoy it is`() {
        val hello = clientHello(serverName = "public.example.net", ech = true)

        assertEquals(ClientHello.Found("public.example.net", echPresent = true), TlsClientHello.parse(hello))
    }

    /** The ECH extension follows the SNI one on the wire, so an early return would miss it. */
    @Test
    fun `ech is still noticed when it comes after the server name`() {
        val hello = clientHello(serverName = "public.example.net", ech = true, echBeforeSni = false)

        assertEquals(ClientHello.Found("public.example.net", echPresent = true), TlsClientHello.parse(hello))
    }

    @Test
    fun `unknown and GREASE extensions are skipped, not tripped over`() {
        val hello = clientHello(
            serverName = "ads.example.com",
            extras = listOf(
                0x0A0A to ByteArray(0),
                0x002B to byteArrayOf(2, 3, 4),
                0xFAFA to ByteArray(200) { 0x5A },
            ),
        )

        assertEquals(ClientHello.Found("ads.example.com", false), TlsClientHello.parse(hello))
    }

    @Test
    fun `a name with a byte that is not printable ASCII is refused, not coerced`() {
        val hello = clientHello(serverName = "ads example.com")

        assertEquals(ClientHello.NoName, TlsClientHello.parse(hello))
    }

    @Test
    fun `the hello is read at an offset inside a larger buffer`() {
        val hello = clientHello()
        val buffer = ByteArray(17) { 0x7F } + hello + ByteArray(9) { 0x7F }

        assertEquals(ClientHello.Found("ads.example.com", false), TlsClientHello.parse(buffer, 17, hello.size))
    }

    @Test
    fun `an empty read is incomplete, not a decision`() {
        assertEquals(ClientHello.Incomplete, TlsClientHello.parse(ByteArray(0)))
    }

    // ---- fixtures ---------------------------------------------------------------------------

    private fun clientHello(
        serverName: String? = "ads.example.com",
        extras: List<Pair<Int, ByteArray>> = emptyList(),
        ech: Boolean = false,
        echBeforeSni: Boolean = true,
        omitExtensionsBlock: Boolean = false,
        recordSize: Int = 0,
    ): ByteArray {
        val extensions = ByteArrayOutputStream()
        if (ech && echBeforeSni) extensions.write(extension(0xFE0D, byteArrayOf(0, 1, 2)))
        if (serverName != null) extensions.write(extension(0x0000, serverNameList(serverName)))
        if (ech && !echBeforeSni) extensions.write(extension(0xFE0D, byteArrayOf(0, 1, 2)))
        for ((type, data) in extras) extensions.write(extension(type, data))

        val body = ByteArrayOutputStream()
        body.write(byteArrayOf(0x03, 0x03))
        body.write(ByteArray(32) { it.toByte() })
        body.write(32)
        body.write(ByteArray(32) { 0x11 })                                  // legacy_session_id
        body.write(u16(4))
        body.write(byteArrayOf(0x13, 0x01, 0x13, 0x02))                     // cipher_suites
        body.write(1)
        body.write(0)                                                       // legacy_compression_methods
        if (!omitExtensionsBlock) {
            val bytes = extensions.toByteArray()
            body.write(u16(bytes.size))
            body.write(bytes)
        }

        val bodyBytes = body.toByteArray()
        val handshake = ByteArrayOutputStream()
        handshake.write(0x01)
        handshake.write(
            byteArrayOf(
                (bodyBytes.size shr 16).toByte(),
                (bodyBytes.size shr 8).toByte(),
                bodyBytes.size.toByte(),
            ),
        )
        handshake.write(bodyBytes)

        return records(handshake.toByteArray(), recordSize)
    }

    private fun records(handshake: ByteArray, recordSize: Int): ByteArray {
        val chunk = if (recordSize <= 0) handshake.size else recordSize
        val out = ByteArrayOutputStream()
        var at = 0
        while (at < handshake.size) {
            val take = minOf(chunk, handshake.size - at)
            out.write(byteArrayOf(0x16, 0x03, 0x01))
            out.write(u16(take))
            out.write(handshake, at, take)
            at += take
        }
        return out.toByteArray()
    }

    private fun extension(type: Int, data: ByteArray): ByteArray {
        val out = ByteArrayOutputStream()
        out.write(u16(type))
        out.write(u16(data.size))
        out.write(data)
        return out.toByteArray()
    }

    private fun serverNameList(name: String): ByteArray {
        val raw = ByteArray(name.length) { name[it].code.toByte() }
        val entry = ByteArrayOutputStream()
        entry.write(0) // host_name
        entry.write(u16(raw.size))
        entry.write(raw)
        val entryBytes = entry.toByteArray()
        val out = ByteArrayOutputStream()
        out.write(u16(entryBytes.size))
        out.write(entryBytes)
        return out.toByteArray()
    }

    private fun u16(value: Int) = byteArrayOf((value shr 8).toByte(), value.toByte())
}
