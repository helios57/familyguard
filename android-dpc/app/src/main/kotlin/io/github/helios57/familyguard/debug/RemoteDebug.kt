package io.github.helios57.familyguard.debug

import android.app.NotificationChannel
import android.app.NotificationManager
import android.content.Context
import android.os.UserManager
import android.security.NetworkSecurityPolicy
import android.util.Log
import androidx.core.app.NotificationCompat
import io.github.helios57.familyguard.R
import io.github.helios57.familyguard.commands.CommandOutcome
import io.github.helios57.familyguard.net.ApiClient
import io.github.helios57.familyguard.net.DebugLegTarget
import io.github.helios57.familyguard.net.HttpUpgrade
import io.github.helios57.familyguard.policy.DpmRestrictionGateway
import java.io.IOException
import java.io.InputStream
import java.io.OutputStream
import java.net.InetAddress
import java.net.InetSocketAddress
import java.net.Socket
import java.util.concurrent.atomic.AtomicInteger
import javax.net.ssl.HttpsURLConnection
import javax.net.ssl.SSLSocket
import javax.net.ssl.SSLSocketFactory
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.contentOrNull
import kotlinx.serialization.json.intOrNull
import kotlinx.serialization.json.jsonPrimitive

/**
 * The phone's half of remote adb (FR-19): connect to this phone's own adbd, dial the server, and
 * splice the two.
 *
 * Nothing here speaks adb. adb's pairing and connection are TLS between the parent's adb client and
 * this phone's adbd, so this class, the server and the network in between all carry bytes they
 * cannot read, and the phone's trust decision — which adb keys it accepts — stays where Android
 * puts it: in the pairing dialog on this screen.
 *
 * ### What has to be true, and who makes it true
 *
 * - **Debugging allowed.** The child's `allow_debugging` lifts `no_debugging_features`. Read here
 *   from the restriction ACTUALLY in force rather than from a cached policy: it is the restriction
 *   that decides whether adbd runs, so it is the only answer that cannot be stale.
 * - **Wireless debugging on.** The device owner asks for it (`adb_wifi_enabled`); what the platform
 *   does with that is read back rather than assumed, and a refusal is reported in its own words.
 * - **This host paired.** Only the phone can show a pairing code, so the first session needs a
 *   person at the phone once. Every later one does not.
 *
 * ### Visible (FR-19.5)
 *
 * A notification stands for as long as any stream is open, at default importance, on its own
 * channel. The child can see that someone is connected to their phone, which is the property a
 * remote shell on somebody else's phone has to have.
 */
class RemoteDebug(
    context: Context,
    private val api: ApiClient,
    private val finder: AdbPortFinder = AdbPortFinder(context),
    /** null when Wireless debugging is on, or why it is not. See the gateway for the read-back. */
    private val switchOn: () -> String? = { DpmRestrictionGateway.switchWirelessDebuggingOn(context) },
) {
    private val context = context.applicationContext
    private val open = AtomicInteger()

    fun open(params: JsonObject): CommandOutcome {
        val stream = params["stream"]?.jsonPrimitive?.contentOrNull.orEmpty()
        val target = params["target"]?.jsonPrimitive?.contentOrNull ?: "connect"
        val explicitPort = params["port"]?.jsonPrimitive?.intOrNull ?: 0
        if (stream.isEmpty()) return CommandOutcome.Failed("the command carried no stream id")
        if (target != "connect" && target != "pair") return CommandOutcome.Failed("unknown debug target '$target'")
        if (open.get() >= MAX_STREAMS) {
            return CommandOutcome.Failed("$MAX_STREAMS debug streams are already open on this phone")
        }

        val users = context.getSystemService(UserManager::class.java)
        if (users?.hasUserRestriction(UserManager.DISALLOW_DEBUGGING_FEATURES) == true) {
            return CommandOutcome.Failed(
                "debugging is switched off on this phone by the policy it has applied; switch on Allow " +
                    "debugging and let the phone sync first"
            )
        }

        val service = if (target == "pair") AdbPortFinder.Service.PAIRING else AdbPortFinder.Service.CONNECT
        var enabledNote: String? = null
        val adbd: Socket = if (explicitPort > 0) {
            connectLocal(listOf(LOOPBACK), explicitPort)
                ?: return CommandOutcome.Failed("nothing on this phone accepts connections on port $explicitPort")
        } else {
            var found = finder.find(service)
            if (found == null && service == AdbPortFinder.Service.CONNECT) {
                enabledNote = switchOn()
                if (enabledNote == null) found = finder.find(service, ENABLE_WAIT_MILLIS)
            }
            if (found == null) {
                return CommandOutcome.Failed(notFound(service, enabledNote))
            }
            connectLocal(listOf(LOOPBACK, found.address), found.port)
                ?: return CommandOutcome.Failed(
                    "adb announced port ${found.port} on this phone and refused a connection to it"
                )
        }

        val leg = try {
            dialServer(api.debugLeg(stream))
        } catch (e: Exception) {
            adbd.closeQuietly()
            return CommandOutcome.Failed("could not open the stream to the server: ${e.message ?: e.javaClass.simpleName}")
        }

        splice(adbd, leg)
        return CommandOutcome.Done(
            buildMap {
                put("state", "connected")
                put("target", target)
                put("port", adbd.port.toString())
                enabledNote?.let { put("note", it) }
            }
        )
    }

    private fun notFound(service: AdbPortFinder.Service, enabledNote: String?): String = when {
        enabledNote != null -> enabledNote
        service == AdbPortFinder.Service.PAIRING ->
            "no pairing service is announced on this phone: open Developer options → Wireless debugging → " +
                "Pair device with pairing code, and keep that screen open"
        else -> "Wireless debugging is on but announced no adb port within " +
            "${(AdbPortFinder.DEFAULT_TIMEOUT_MILLIS + ENABLE_WAIT_MILLIS) / 1000} s; the phone may be off " +
            "Wi-Fi, or waiting for someone to allow debugging on this network on its screen"
    }

    private fun connectLocal(addresses: List<InetAddress>, port: Int): Socket? {
        for (address in addresses.distinct()) {
            val socket = Socket()
            try {
                socket.connect(InetSocketAddress(address, port), CONNECT_TIMEOUT_MILLIS)
                socket.tcpNoDelay = true
                return socket
            } catch (e: IOException) {
                socket.closeQuietly()
                Log.d(TAG, "adbd not reachable at $address:$port: ${e.message}")
            }
        }
        return null
    }

    /**
     * The server leg: TLS with the platform's trust anchors and hostname check, or plain HTTP only
     * where this app's network security policy allows cleartext to that host — i.e. the debug build
     * talking to an emulator's host. A raw socket bypasses that policy unless it is asked, so it is.
     */
    private fun dialServer(target: DebugLegTarget): Leg {
        val socket: Socket = if (target.tls) {
            // A plain socket first, so the connect has a timeout; TLS layered over it with the
            // host name, which is what carries SNI to ingress-nginx.
            val plain = Socket()
            plain.connect(InetSocketAddress(target.host, target.port), CONNECT_TIMEOUT_MILLIS)
            val layered = (SSLSocketFactory.getDefault() as SSLSocketFactory)
                .createSocket(plain, target.host, target.port, true) as SSLSocket
            layered.startHandshake()
            if (!HttpsURLConnection.getDefaultHostnameVerifier().verify(target.host, layered.session)) {
                layered.closeQuietly()
                throw IOException("the server's certificate is not valid for ${target.host}")
            }
            layered
        } else {
            if (!NetworkSecurityPolicy.getInstance().isCleartextTrafficPermitted(target.host)) {
                throw IOException("cleartext to ${target.host} is not permitted by this build")
            }
            Socket().apply { connect(InetSocketAddress(target.host, target.port), CONNECT_TIMEOUT_MILLIS) }
        }
        try {
            socket.soTimeout = HANDSHAKE_TIMEOUT_MILLIS
            val input = socket.getInputStream()
            val output = socket.getOutputStream()
            HttpUpgrade.send(output, target.request)
            val head = HttpUpgrade.readHead(input)
            if (head.status != 101) throw IOException(HttpUpgrade.refusal(head, input))
            socket.soTimeout = 0
            socket.tcpNoDelay = true
            return Leg(socket, input, output)
        } catch (e: Exception) {
            socket.closeQuietly()
            throw e
        }
    }

    private class Leg(val socket: Socket, val input: InputStream, val output: OutputStream)

    private fun splice(adbd: Socket, server: Leg) {
        val streams = open.incrementAndGet()
        notify(streams)
        val closed = java.util.concurrent.atomic.AtomicBoolean(false)
        val closeBoth = {
            if (closed.compareAndSet(false, true)) {
                adbd.closeQuietly()
                server.socket.closeQuietly()
                notify(open.decrementAndGet())
                Log.i(TAG, "debug stream closed")
            }
        }
        pump("fg-debug-up", server.input, adbd.getOutputStream(), closeBoth)
        pump("fg-debug-down", adbd.getInputStream(), server.output, closeBoth)
        Log.i(TAG, "debug stream open to adbd port ${adbd.port}")
    }

    private fun pump(name: String, from: InputStream, to: OutputStream, done: () -> Unit) {
        Thread({
            try {
                val buffer = ByteArray(16 * 1024)
                while (true) {
                    val n = from.read(buffer)
                    if (n < 0) break
                    to.write(buffer, 0, n)
                    to.flush()
                }
            } catch (ignored: IOException) {
                // The other side closed it, which is how every stream ends.
            } finally {
                done()
            }
        }, name).apply { isDaemon = true }.start()
    }

    private fun notify(streams: Int) {
        val manager = context.getSystemService(NotificationManager::class.java) ?: return
        if (streams <= 0) {
            manager.cancel(NOTIFICATION_ID)
            return
        }
        manager.createNotificationChannel(
            NotificationChannel(CHANNEL, context.getString(R.string.debug_channel), NotificationManager.IMPORTANCE_DEFAULT),
        )
        manager.notify(
            NOTIFICATION_ID,
            NotificationCompat.Builder(context, CHANNEL)
                .setSmallIcon(android.R.drawable.stat_sys_warning)
                .setContentTitle(context.getString(R.string.debug_connected_title))
                .setContentText(context.getString(R.string.debug_connected_text))
                .setOngoing(true)
                .setOnlyAlertOnce(true)
                .setVisibility(NotificationCompat.VISIBILITY_PUBLIC)
                .build(),
        )
    }

    private fun Socket.closeQuietly() {
        try {
            close()
        } catch (ignored: IOException) {
        }
    }

    companion object {
        private const val TAG = "FGDebug"
        private const val CHANNEL = "remote-debugging"
        private const val NOTIFICATION_ID = 0x46474442

        private val LOOPBACK: InetAddress = InetAddress.getByName("127.0.0.1")
        private const val CONNECT_TIMEOUT_MILLIS = 5_000
        private const val HANDSHAKE_TIMEOUT_MILLIS = 15_000
        private const val ENABLE_WAIT_MILLIS = 8_000L
        private const val MAX_STREAMS = 4
    }
}
