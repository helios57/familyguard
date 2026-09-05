package io.github.helios57.familyguard.net

import io.github.helios57.familyguard.enforce.DesiredState
import io.github.helios57.familyguard.enforce.Input
import java.io.IOException
import java.io.InputStream
import java.net.HttpURLConnection
import java.net.URL
import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.Json

/** The server answered, and its answer was a refusal. Carries what the server said it was. */
class ApiException(
    val status: Int,
    val code: String,
    val detail: String,
    val requestId: String,
) : IOException("HTTP $status $code: $detail${if (requestId.isEmpty()) "" else " (request $requestId)"}") {

    /**
     * Whether retrying this request unchanged could ever succeed.
     *
     * The distinction is the difference between a phone that reconnects through a backend restart
     * and one that retries a revoked credential every thirty seconds until the battery dies. 401
     * and 403 mean this device's token is no longer accepted, and no amount of waiting changes
     * that; 409 at enrollment means the token was single-use and is spent. Everything else — 5xx,
     * 429, a timeout — is worth another attempt later.
     */
    val retryable: Boolean
        get() = status !in setOf(400, 401, 403, 404, 409, 422)
}

@Serializable
private data class ErrorBody(
    @SerialName("error") val error: String = "",
    @SerialName("message") val message: String = "",
    @SerialName("request_id") val requestId: String = "",
)

@Serializable
data class EnrollRequest(
    @SerialName("enrollment_token") val enrollmentToken: String,
    @SerialName("model") val model: String = "",
    @SerialName("os_version") val osVersion: String = "",
    @SerialName("critical_packages") val criticalPackages: List<String> = emptyList(),
)

@Serializable
data class RecoveryMaterial(
    @SerialName("salt") val salt: String = "",
    @SerialName("iterations") val iterations: Int = 0,
    @SerialName("hash") val hash: String = "",
)

@Serializable
data class EnrollResponse(
    @SerialName("device_token") val deviceToken: String = "",
    @SerialName("device_id") val deviceId: String = "",
    @SerialName("child_id") val childId: String = "",
    @SerialName("recovery") val recovery: RecoveryMaterial = RecoveryMaterial(),
)

/**
 * What `/device/policy` returns: the server's own computation *and* the inputs it computed from.
 *
 * Both are needed, and for different reasons. [desired] is what the console is showing the parent
 * right now, so applying it keeps the two in agreement. [input] is what lets this phone keep
 * enforcing bedtime and the daily quota with no network at all (FR-9) — the local engine recomputes
 * from it as the clock moves, which is something a stored desired state cannot do, because
 * "bedtime starts in ten minutes" is not a fact, it is a function of the time.
 */
@Serializable
data class PolicyResponse(
    @SerialName("desired") val desired: DesiredState = DesiredState(),
    @SerialName("input") val input: Input = Input(),
)

@Serializable
data class HeartbeatRequest(
    @SerialName("battery_level") val batteryLevel: Int? = null,
    @SerialName("charging") val charging: Boolean? = null,
    @SerialName("screen_on") val screenOn: Boolean? = null,
    @SerialName("connectivity") val connectivity: String = "",
    @SerialName("policy_version") val policyVersion: Long = 0,
    /**
     * The DPC build running on this phone (FR-15.4).
     *
     * Sent on every heartbeat rather than once at enrollment, because the value changes without an
     * enrollment: an `UPDATE_APP` replaces this app, and the acknowledgement for that command went
     * out *before* the install — so this field is the only thing that ever reports the update
     * actually took effect.
     */
    @SerialName("app_version_name") val appVersionName: String = "",
    @SerialName("app_version_code") val appVersionCode: Long = 0,
)

@Serializable
data class HeartbeatResponse(
    @SerialName("policy_version") val policyVersion: Long = 0,
    @SerialName("pending_commands") val pendingCommands: Int = 0,
)

/**
 * What the server says this phone should be running (`GET /device/apk-info`).
 *
 * [packageChecksum] is the SHA-256 of the APK the server serves, url-safe base64 without padding —
 * the same value and the same encoding as the provisioning QR's package checksum, because it is
 * literally the same number computed by the same code.
 */
@Serializable
data class ApkInfoResponse(
    @SerialName("url") val url: String = "",
    @SerialName("package_checksum") val packageChecksum: String = "",
    @SerialName("size") val size: Long = 0,
)

@Serializable
data class InventoryApp(
    @SerialName("package_name") val packageName: String,
    @SerialName("label") val label: String = "",
    @SerialName("system_app") val systemApp: Boolean = false,
)

@Serializable
data class InventoryRequest(
    @SerialName("apps") val apps: List<InventoryApp> = emptyList(),
)

@Serializable
data class InventoryResponse(
    @SerialName("apps") val apps: Int = 0,
)

/**
 * One day's cumulative per-package foreground milliseconds.
 *
 * [day] is sent explicitly rather than left to the server. The server's own default is "today in the
 * child's timezone", which is right for a report that describes the moment it is sent — but a poll
 * that runs at 00:02 is reporting a window that mostly belongs to yesterday, and letting the server
 * date it would move those minutes onto a quota that has just reset.
 */
@Serializable
data class UsageRequest(
    @SerialName("day") val day: String = "",
    @SerialName("samples") val samples: Map<String, Long> = emptyMap(),
)

@Serializable
data class UsageResponse(
    @SerialName("day") val day: String = "",
    @SerialName("minutes") val minutes: Int = 0,
)

/**
 * One instant command the parent issued (FR-9).
 *
 * `params` is deliberately not modelled. The server's column is `map[string]any` and no command type
 * in `store.ValidCommandTypes` carries one today, so a field here would be an empty map on every
 * command — and typing it as `Map<String, String>` would make the *first* command that carried a
 * number fail to parse, which on this path means the whole batch is dropped rather than one command
 * refused. When a type gains a parameter, adding it here is the same commit that reads it.
 */
@Serializable
data class DeviceCommand(
    @SerialName("id") val id: String = "",
    @SerialName("type") val type: String = "",
)

@Serializable
data class CommandsResponse(
    @SerialName("commands") val commands: List<DeviceCommand> = emptyList(),
)

/**
 * What a command id is allowed to look like.
 *
 * One definition, two uses: [ApiClient.ackCommand] refuses to build a path from anything else, and
 * the executor refuses to *run* anything else — because a command that cannot be acknowledged is a
 * command that can be neither reported nor stopped, and `TRIGGER_ALARM` is in the set.
 */
object CommandId {
    private val PATTERN =
        Regex("^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$")

    fun isValid(id: String): Boolean = PATTERN.matches(id)
}

@Serializable
data class AckRequest(
    @SerialName("ok") val ok: Boolean,
    @SerialName("result") val result: Map<String, String> = emptyMap(),
    @SerialName("error") val error: String = "",
)

/**
 * One position fix, for `LOCATE_NOW` (FR-9).
 *
 * [capturedAt] is the instant the *fix* was taken, not the instant it was sent. They differ whenever
 * the answer came from the platform's last known location, and the difference is the whole value of
 * the field: a parent looking at a map needs to know they are being shown where the phone was twenty
 * minutes ago. The server stores what it is given, so sending "now" here would erase that.
 */
@Serializable
data class LocationRequest(
    @SerialName("latitude") val latitude: Double,
    @SerialName("longitude") val longitude: Double,
    @SerialName("accuracy_m") val accuracyM: Double? = null,
    @SerialName("captured_at") val capturedAt: String = "",
)

/**
 * One use of the offline recovery code, reported when the device can reach the server again
 * (FR-12.5).
 *
 * [occurredAt] is when the code was *typed*, not when this request was sent, and the two are
 * routinely days apart: the whole point of the recovery path is that it works with no network. A
 * device that let the server stamp it would file every attempt at the moment the phone came back
 * online, which is the one instant that is certainly wrong.
 */
@Serializable
data class RecoveryEventRequest(
    @SerialName("succeeded") val succeeded: Boolean,
    @SerialName("occurred_at") val occurredAt: String = "",
)

/**
 * The device's half of the REST API.
 *
 * Built on `HttpURLConnection` rather than a client library on purpose: this app already ships a
 * device-owner APK that a parent installs on a child's phone from a QR code, and every dependency
 * added here is code running with device-owner authority. The whole surface needed is four verbs
 * over JSON.
 *
 * [token] is read per request rather than captured, because enrollment replaces it while the same
 * client instance is alive.
 */
class ApiClient(
    baseUrl: String,
    private val token: () -> String?,
    private val connectTimeoutMillis: Int = 15_000,
    private val readTimeoutMillis: Int = 30_000,
    private val openConnection: (URL) -> HttpURLConnection = { it.openConnection() as HttpURLConnection },
) {
    /**
     * Normalised once, at construction. A base URL with a trailing slash and a path that starts
     * with one produce `//api/v1/...`, which some proxies route and others 404 — a difference that
     * shows up as "works in dev, fails at the customer" and nowhere in between.
     */
    val baseUrl: String = baseUrl.trimEnd('/')

    private val json = Json {
        ignoreUnknownKeys = true
        encodeDefaults = true
        explicitNulls = false
    }

    fun enroll(request: EnrollRequest): EnrollResponse =
        post("/api/v1/enroll", json.encodeToString(EnrollRequest.serializer(), request), authenticated = false)
            .let { json.decodeFromString(EnrollResponse.serializer(), it) }

    fun policy(): PolicyResponse =
        get("/api/v1/device/policy").let { json.decodeFromString(PolicyResponse.serializer(), it) }

    fun heartbeat(request: HeartbeatRequest): HeartbeatResponse =
        post("/api/v1/device/heartbeat", json.encodeToString(HeartbeatRequest.serializer(), request))
            .let { json.decodeFromString(HeartbeatResponse.serializer(), it) }

    /** @return the number of apps the *server* says it stored, which is not always what was sent. */
    fun reportInventory(request: InventoryRequest): Int =
        post("/api/v1/device/inventory", json.encodeToString(InventoryRequest.serializer(), request))
            .let { json.decodeFromString(InventoryResponse.serializer(), it).apps }

    fun reportUsage(request: UsageRequest): UsageResponse =
        post("/api/v1/device/usage", json.encodeToString(UsageRequest.serializer(), request))
            .let { json.decodeFromString(UsageResponse.serializer(), it) }

    /**
     * Drains the queue.
     *
     * **This call is what records delivery**, on the server, for every command it returns — the push
     * event says only "there is something to fetch". So a device that fetches and then dies has
     * commands the console shows as delivered and never acknowledged, which is the honest reading;
     * one that treated the wake-up as the delivery would show a phone in a tunnel as having received
     * an alarm it never got.
     */
    fun commands(): List<DeviceCommand> =
        get("/api/v1/device/commands")
            .let { json.decodeFromString(CommandsResponse.serializer(), it).commands }

    /**
     * Reports what this device did with one command. The response is the updated row; unused.
     *
     * The id goes into the request *path*, so it is checked rather than trusted. It arrives from the
     * server and the server generates UUIDs — but "the value came from a trusted source" is exactly
     * the reasoning that puts a `?` or a `../` into a URL, and here that would silently address a
     * different endpoint with this device's bearer token on it.
     */
    fun ackCommand(id: String, request: AckRequest) {
        require(CommandId.isValid(id)) { "refusing to build a request path from a non-UUID command id" }
        post("/api/v1/device/commands/$id/ack", json.encodeToString(AckRequest.serializer(), request))
    }

    /**
     * What DPC this server hosts, so this phone can decide whether to replace itself with it.
     *
     * A 404 here is a normal answer, not an outage: a control plane may host no DPC at all. It
     * arrives as an [ApiException] the caller reports as the refusal it is.
     */
    fun apkInfo(): ApkInfoResponse =
        get("/api/v1/device/apk-info").let { json.decodeFromString(ApkInfoResponse.serializer(), it) }

    fun reportLocation(request: LocationRequest) {
        post("/api/v1/device/location", json.encodeToString(LocationRequest.serializer(), request))
    }

    fun reportRecoveryEvent(request: RecoveryEventRequest) {
        post(
            "/api/v1/device/recovery-event",
            json.encodeToString(RecoveryEventRequest.serializer(), request),
        )
    }

    /**
     * Opens the event stream and hands back the live connection.
     *
     * Separate from the request helpers because a stream must not carry a read timeout: the server
     * holds the connection open for fifteen minutes and writes a keepalive every twenty seconds, so
     * any read deadline shorter than the keepalive interval turns a healthy idle stream into a
     * reconnect loop, and one longer than it merely delays noticing a dead one. The keepalive is the
     * liveness signal; the socket timeout is set just above it so a stream that stops producing even
     * those is torn down rather than held forever.
     */
    fun openStream(): HttpURLConnection {
        val connection = connect("/api/v1/device/stream", "GET", authenticated = true)
        connection.readTimeout = STREAM_READ_TIMEOUT_MILLIS
        connection.setRequestProperty("Accept", "text/event-stream")
        connection.connect()
        val status = connection.responseCode
        if (status != HttpURLConnection.HTTP_OK) {
            val body = readAll(connection.errorStream)
            connection.disconnect()
            throw asApiException(status, body)
        }
        return connection
    }

    /**
     * Opens an authenticated download of an absolute URL the server named, and refuses to send this
     * device's credential anywhere else.
     *
     * The managed-app APKs (FR-16.3) are served from a device-authenticated route, so unlike
     * `/dpc.apk` — which a factory-reset phone fetches with no credential at all — this one has to
     * carry the bearer. And the URL comes out of the desired state, which is to say out of the
     * network: attaching a token to whatever absolute URL arrived is how a token leaves the
     * deployment it belongs to, and it would leave silently, to a host that answered 200.
     *
     * So the origin is checked against this client's own base URL and the credential is sent only
     * on a match. Compared as scheme + host + port rather than as a string prefix, because
     * `https://guard.example.com.attacker.test/` is a prefix match on `https://guard.example.com`
     * and a completely different server. A mismatch throws rather than downloading anonymously:
     * this route answers 401 without the token, so an anonymous attempt would fail later, further
     * from the reason, and after the bytes.
     */
    fun openDownload(url: String): InputStream {
        val target = try {
            URL(url)
        } catch (e: Exception) {
            throw IOException("the server named a download URL this device cannot parse: $url")
        }
        val base = URL(baseUrl)
        if (!sameOrigin(base, target)) {
            throw IOException(
                "refusing to send this device's credential to ${origin(target)}; this device is " +
                    "enrolled with ${origin(base)}"
            )
        }
        val connection = openConnection(target)
        connection.requestMethod = "GET"
        connection.connectTimeout = connectTimeoutMillis
        // A managed app can be tens of megabytes on a phone's connection, and the read timeout is
        // per read rather than for the whole transfer — but 30s of no bytes at all on a download
        // that is progressing does not happen, and a longer one only delays noticing a dead socket.
        connection.readTimeout = readTimeoutMillis
        connection.setRequestProperty("Accept", "application/vnd.android.package-archive")
        val bearer = token()
            ?: throw IllegalStateException("GET $url needs a device token and this device has none")
        connection.setRequestProperty("Authorization", "Bearer $bearer")
        val status = connection.responseCode
        if (status !in 200..299) {
            val body = readAll(connection.errorStream)
            connection.disconnect()
            throw asApiException(status, body)
        }
        return connection.inputStream
    }

    /** Scheme, host and port — the three things that decide which server this is. */
    private fun sameOrigin(a: URL, b: URL): Boolean =
        a.protocol.equals(b.protocol, ignoreCase = true) &&
            a.host.equals(b.host, ignoreCase = true) &&
            effectivePort(a) == effectivePort(b)

    /** `URL.getPort()` is -1 when the URL omits it, so :443 and an implicit 443 must not differ. */
    private fun effectivePort(u: URL): Int = if (u.port == -1) u.defaultPort else u.port

    private fun origin(u: URL): String = "${u.protocol}://${u.host}:${effectivePort(u)}"

    private fun get(path: String): String {
        val connection = connect(path, "GET", authenticated = true)
        return complete(connection)
    }

    private fun post(path: String, body: String, authenticated: Boolean = true): String {
        val connection = connect(path, "POST", authenticated)
        connection.doOutput = true
        connection.setRequestProperty("Content-Type", "application/json")
        connection.outputStream.use { it.write(body.toByteArray(Charsets.UTF_8)) }
        return complete(connection)
    }

    private fun connect(path: String, method: String, authenticated: Boolean): HttpURLConnection {
        val connection = openConnection(URL(baseUrl + path))
        connection.requestMethod = method
        connection.connectTimeout = connectTimeoutMillis
        connection.readTimeout = readTimeoutMillis
        connection.setRequestProperty("Accept", "application/json")
        if (authenticated) {
            val bearer = token()
                ?: throw IllegalStateException("$method $path needs a device token and this device has none")
            connection.setRequestProperty("Authorization", "Bearer $bearer")
        }
        return connection
    }

    private fun complete(connection: HttpURLConnection): String {
        try {
            val status = connection.responseCode
            // Read the error stream, not the input stream, on a failure: HttpURLConnection throws
            // from getInputStream() for any 4xx or 5xx, and the exception carries none of the
            // server's own explanation. The whole point of the error envelope is to say which of
            // eleven possible refusals this was.
            if (status !in 200..299) throw asApiException(status, readAll(connection.errorStream))
            return readAll(connection.inputStream)
        } finally {
            connection.disconnect()
        }
    }

    private fun asApiException(status: Int, body: String): ApiException {
        val parsed = runCatching { json.decodeFromString(ErrorBody.serializer(), body) }.getOrNull()
        return ApiException(
            status = status,
            code = parsed?.error?.takeIf { it.isNotEmpty() } ?: "http_$status",
            // A body that is not the envelope is far more likely to be a proxy's HTML error page
            // than anything readable, so it is truncated rather than carried into a log line.
            detail = parsed?.message?.takeIf { it.isNotEmpty() } ?: body.take(200),
            requestId = parsed?.requestId.orEmpty(),
        )
    }

    private fun readAll(stream: InputStream?): String =
        stream?.use { it.readBytes().toString(Charsets.UTF_8) } ?: ""

    companion object {
        /**
         * Just above the server's 20s keepalive. See [openStream] — this is a liveness deadline for
         * a stream that has gone silent, not a request timeout.
         */
        const val STREAM_READ_TIMEOUT_MILLIS = 45_000
    }
}
