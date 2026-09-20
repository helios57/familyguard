package io.github.helios57.familyguard.filter

/**
 * A connection out of the tunnel to the destination an app actually asked for.
 *
 * Kept as an interface with no socket in it so [PacketRouter] can be driven by a test that owns no
 * network at all, and so the one place that has to call `VpnService.protect()` is a single
 * implementation rather than a rule spread across the routing code. Forgetting to protect a socket
 * does not fail: the packets go back into the tunnel that created them, the connection loops until
 * something times out, and the symptom is an app that is simply slow.
 */
interface Upstream {
    /** Queue bytes for the destination. Whatever cannot be written now is written later, in order. */
    fun send(bytes: ByteArray)

    /** The app is done sending. TCP half-close; a no-op for UDP. */
    fun closeSend()

    /** Drop the connection. Safe to call twice. */
    fun close()
}

/** What one connection tells the router about itself, always on the router's own thread. */
interface UpstreamListener {
    fun onConnected()

    /** [bytes] is only valid for the duration of the call; copy anything kept past it. */
    fun onData(bytes: ByteArray, offset: Int, length: Int)

    /** The destination closed cleanly. */
    fun onClosed()

    /** The destination could never be reached, or the connection broke. */
    fun onFailed(reason: String)
}

/** Where connections come from. The Android implementation is the one that calls `protect()`. */
interface UpstreamOpener {
    fun openTcp(address: ByteArray, port: Int, listener: UpstreamListener): Upstream?

    fun openUdp(address: ByteArray, port: Int, listener: UpstreamListener): Upstream?
}
