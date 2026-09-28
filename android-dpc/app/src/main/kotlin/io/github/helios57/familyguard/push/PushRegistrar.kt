package io.github.helios57.familyguard.push

import android.content.Context
import android.util.Log
import com.google.firebase.FirebaseApp
import com.google.firebase.FirebaseOptions
import com.google.firebase.installations.FirebaseInstallations
import com.google.firebase.messaging.FirebaseMessaging
import kotlinx.serialization.json.Json

/**
 * Firebase, started from the options the server sent rather than from a google-services.json
 * (FR-26.3), and what this phone knows about its own push: the token, and whether a push has arrived
 * for it.
 *
 * The "token" is the Firebase Installation ID. firebase-messaging 25.1 deprecated the registration
 * token in favour of registering the installation (`register()`, `onRegistered`), and FCM's send API
 * now addresses a phone by `fid`; the heartbeat keeps its field name `push_token`.
 *
 * Kept in plain app-private preferences. The token lets nobody do anything without the server's
 * service-account key, and the options are public by design.
 */
object PushRegistrar {
    private const val TAG = "FamilyGuard/Push"
    private const val PREFS = "push"
    private const val KEY_OPTIONS = "options"
    private const val KEY_TOKEN = "token"
    private const val KEY_VERIFIED = "verified"

    private val json = Json { ignoreUnknownKeys = true }

    /** What Firebase runs with in this process, or null when it is not started. */
    @Volatile private var running: PushOptions? = null

    private fun prefs(context: Context) = context.getSharedPreferences(PREFS, Context.MODE_PRIVATE)

    private fun stored(context: Context): PushOptions? = prefs(context).getString(KEY_OPTIONS, null)
        ?.let { runCatching { json.decodeFromString(PushOptions.serializer(), it) }.getOrNull() }

    fun token(context: Context): String = prefs(context).getString(KEY_TOKEN, "") ?: ""

    /** The next safety poll's distance (FR-26.2, FR-26.3). */
    fun pollMillis(context: Context): Long =
        PushPolicy.pollMillis(token(context), prefs(context).getString(KEY_VERIFIED, "") ?: "")

    /** The heartbeat's `push_token`. */
    fun reportedToken(context: Context): String? = PushPolicy.reportedToken(stored(context), token(context))

    /**
     * From `Application.onCreate`: a push can be what starts this process, and Firebase has to be up to
     * hand it over. Uses the options the last sync kept.
     */
    fun resume(context: Context) {
        runCatching { configure(context, stored(context)) {} }
            .onFailure { Log.w(TAG, "push could not resume: ${it.message}") }
    }

    /** A push arrived: the token this phone holds is proven to work. */
    fun received(context: Context) {
        val token = token(context)
        if (token.isNotEmpty()) prefs(context).edit().putString(KEY_VERIFIED, token).apply()
    }

    /** Firebase handed out a new token; the previous one's proof no longer counts. */
    fun tokenChanged(context: Context, token: String): Boolean {
        if (token == token(context)) return false
        prefs(context).edit().putString(KEY_TOKEN, token).remove(KEY_VERIFIED).apply()
        return true
    }

    /**
     * Starts, keeps, restarts or stops Firebase to match what the server sent. [onNewToken] runs when a
     * token arrives that the server has not been told of, so the caller can send a heartbeat.
     */
    @Synchronized
    fun configure(context: Context, wanted: PushOptions?, onNewToken: () -> Unit) {
        val app = context.applicationContext
        when (PushPolicy.plan(running, wanted)) {
            PushPolicy.Plan.KEEP -> if (running != null && token(app).isEmpty()) fetchToken(app, onNewToken)
            PushPolicy.Plan.START -> {
                start(app, wanted!!)
                fetchToken(app, onNewToken)
            }
            PushPolicy.Plan.RESTART -> {
                stop(app)
                start(app, wanted!!)
                fetchToken(app, onNewToken)
            }
            PushPolicy.Plan.STOP -> stop(app)
        }
    }

    private fun start(context: Context, options: PushOptions) {
        if (FirebaseApp.getApps(context).none { it.name == FirebaseApp.DEFAULT_APP_NAME }) {
            FirebaseApp.initializeApp(
                context,
                FirebaseOptions.Builder()
                    .setProjectId(options.projectId)
                    .setApplicationId(options.applicationId)
                    .setApiKey(options.apiKey)
                    .setGcmSenderId(options.senderId)
                    .build(),
            )
        }
        running = options
        val encoded = json.encodeToString(PushOptions.serializer(), options)
        if (prefs(context).getString(KEY_OPTIONS, null) != encoded) {
            // Another project's token is worth nothing here.
            prefs(context).edit().putString(KEY_OPTIONS, encoded).remove(KEY_TOKEN).remove(KEY_VERIFIED).apply()
        }
        Log.i(TAG, "push: registered with project ${options.projectId}")
    }

    private fun stop(context: Context) {
        runCatching { FirebaseMessaging.getInstance().unregister() }
        FirebaseApp.getApps(context).firstOrNull { it.name == FirebaseApp.DEFAULT_APP_NAME }?.let { runCatching { it.delete() } }
        running = null
        prefs(context).edit().clear().apply()
        Log.i(TAG, "push: off; this phone polls every ${PushPolicy.POLL_WITHOUT_PUSH_MILLIS / 60_000} min")
    }

    /**
     * Registers this installation with FCM, then reads its ID. Only after the registration succeeds:
     * an ID FCM has not registered is an address nothing can be delivered to. [PushWakeService]'s
     * `onRegistered` reports the same ID when Firebase re-registers on its own.
     */
    private fun fetchToken(context: Context, onNewToken: () -> Unit) {
        FirebaseMessaging.getInstance().register().addOnCompleteListener { registered ->
            if (!registered.isSuccessful) {
                // Commonly Google Play services missing or disabled: the phone keeps polling every five minutes.
                Log.w(TAG, "push: not registered (${registered.exception?.message}); polling every 5 min")
                return@addOnCompleteListener
            }
            FirebaseInstallations.getInstance().id.addOnCompleteListener { id ->
                if (!id.isSuccessful) {
                    Log.w(TAG, "push: registered, but no installation id (${id.exception?.message})")
                    return@addOnCompleteListener
                }
                if (tokenChanged(context, id.result)) onNewToken()
            }
        }
    }

    /** FCM dropped this installation's registration: nothing can wake the phone until it registers again. */
    fun unregistered(context: Context) {
        prefs(context).edit().remove(KEY_TOKEN).remove(KEY_VERIFIED).apply()
    }
}
