package io.github.helios57.familyguard.sync

import android.Manifest
import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.app.admin.DevicePolicyManager
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.content.IntentFilter
import android.content.pm.ServiceInfo
import android.net.ConnectivityManager
import android.net.NetworkCapabilities
import android.net.Uri
import android.os.BatteryManager
import android.os.Build
import android.os.Bundle
import android.os.IBinder
import android.os.PersistableBundle
import android.os.PowerManager
import android.os.SystemClock
import android.provider.Settings
import android.util.Log
import androidx.core.app.NotificationCompat
import androidx.core.app.ServiceCompat
import androidx.core.content.ContextCompat
import androidx.core.content.IntentCompat
import io.github.helios57.familyguard.BuildConfig
import io.github.helios57.familyguard.R
import io.github.helios57.familyguard.admin.AdminReceiver
import io.github.helios57.familyguard.commands.AndroidLocationSource
import io.github.helios57.familyguard.commands.AndroidSirenDevice
import io.github.helios57.familyguard.commands.CommandExecutor
import io.github.helios57.familyguard.commands.CommandHandlers
import io.github.helios57.familyguard.commands.CommandQueue
import io.github.helios57.familyguard.commands.HandlerSirenTimer
import io.github.helios57.familyguard.commands.LocationProbe
import io.github.helios57.familyguard.commands.SirenController
import io.github.helios57.familyguard.device.CriticalPackages
import io.github.helios57.familyguard.device.PlatformInstalledAppReader
import io.github.helios57.familyguard.enforce.AlarmDecision
import io.github.helios57.familyguard.enforce.DesiredState
import io.github.helios57.familyguard.enforce.EnforcementAlarm
import io.github.helios57.familyguard.enforce.Input
import io.github.helios57.familyguard.enroll.CredentialStore
import io.github.helios57.familyguard.enroll.Credentials
import io.github.helios57.familyguard.enroll.DeviceFacts
import io.github.helios57.familyguard.enroll.EncryptedCredentialStore
import io.github.helios57.familyguard.enroll.EnrollResult
import io.github.helios57.familyguard.enroll.Enroller
import io.github.helios57.familyguard.enroll.androidDeviceFacts
import io.github.helios57.familyguard.net.AckRequest
import io.github.helios57.familyguard.net.ApiClient
import io.github.helios57.familyguard.net.Backoff
import io.github.helios57.familyguard.net.EventStream
import io.github.helios57.familyguard.net.InventoryApp
import io.github.helios57.familyguard.net.InventoryRequest
import io.github.helios57.familyguard.net.RecoveryEventRequest
import io.github.helios57.familyguard.net.UsageRequest
import io.github.helios57.familyguard.policy.DeviceOwnerPolicy
import io.github.helios57.familyguard.recovery.AndroidRecoveryStore
import io.github.helios57.familyguard.recovery.LinkRefused
import io.github.helios57.familyguard.recovery.RecoveryActivity
import io.github.helios57.familyguard.recovery.RecoveryJournal
import io.github.helios57.familyguard.recovery.RecoveryMode
import io.github.helios57.familyguard.store.encryptedPreferences
import io.github.helios57.familyguard.update.AndroidInstaller
import io.github.helios57.familyguard.update.ApkInfo
import io.github.helios57.familyguard.update.AppUpdater
import io.github.helios57.familyguard.usage.DayAttribution
import io.github.helios57.familyguard.usage.EncryptedUsageStore
import io.github.helios57.familyguard.usage.ScreenOnClock
import io.github.helios57.familyguard.usage.UsageAccess
import io.github.helios57.familyguard.usage.UsageLedger
import io.github.helios57.familyguard.usage.UsageStatsForegroundReader
import io.github.helios57.familyguard.usage.UsageTick
import io.github.helios57.familyguard.usage.UsageTracker
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.launch
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import kotlinx.coroutines.withContext
import java.io.IOException
import java.net.URL
import java.time.Instant
import java.time.OffsetDateTime
import java.time.ZoneId
import java.time.format.DateTimeFormatter

/**
 * The one long-lived component: it enrolls if it must, holds the event stream open, and syncs.
 *
 * A foreground service rather than a periodic job, because the product's whole responsiveness claim
 * rests on it. A parent who taps "lock now" expects the phone to lock, not to lock at the next
 * fifteen-minute window — and a `WorkManager` job cannot hold a socket open at all.
 *
 * **`specialUse`, not `dataSync`.** `dataSync` is the type this looks like, and from Android 15 it
 * is limited to six hours in any twenty-four: a DPC whose connection dies every evening and comes
 * back the next morning is precisely the silent failure this project keeps building guards against,
 * and it would arrive with a `targetSdk` bump rather than with a code change. `specialUse` carries
 * no timeout, and the subtype in the manifest states what the use is. This app is installed from a
 * provisioning QR, never from Play, so the review that gates `specialUse` there does not apply.
 *
 * All the judgement lives in [Synchronizer] and [Enroller], which are covered by JVM tests. What is
 * left here is lifecycle and platform reads, and it is written so that every branch either does
 * something or logs why it did not.
 */
class ConnectionService : Service() {

    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Default)
    private var job: Job? = null

    /**
     * One sync at a time. Two triggers can now fire at once — a server event and a package install —
     * and an applier is read-decide-write against the platform, so interleaving two would let one
     * plan against a device the other has half changed.
     */
    private val syncLock = Mutex()

    /**
     * The provisioning extras, kept across restarts of the loop rather than captured by it.
     *
     * `onStartCommand` can be called again while the loop is running — the compliance activity and
     * the admin receiver both start this service, and `START_STICKY` restarts it after a kill — and
     * the call that carries the enrollment token is not necessarily the first one.
     */
    @Volatile
    private var pendingExtras: Map<String, String?> = emptyMap()

    /**
     * The synchronizer of the running loop, or null before it is up.
     *
     * Held because the enforcement alarm arrives as a fresh `onStartCommand` rather than through the
     * loop, and re-creating a synchronizer for it would mean a second policy cache and a second
     * applier racing the first one against the same device.
     */
    @Volatile
    private var live: Synchronizer? = null

    /**
     * The recovery attempts waiting to be reported (FR-12.5), or null before the loop is up.
     *
     * A field for the same reason as [live]: the screen that records an attempt runs in its own
     * process state and writes to the encrypted store, and this is the only component that ever has
     * a working `ApiClient` to deliver them with.
     */
    @Volatile
    private var journal: RecoveryJournal? = null

    /**
     * The wake-up that makes a bedtime start on a phone lying face down (FR-4.2, NFR-10).
     *
     * `lazy` rather than a constructor argument: a Service is created by the platform, so this is
     * where its dependencies get built, and nothing must touch `AlarmManager` before `onCreate`.
     */
    private val alarm by lazy {
        EnforcementAlarm(AlarmManagerPlatform(this)) { System.currentTimeMillis() }
    }

    /**
     * The find-my-phone siren (FR-9), held by the service and not by the connection loop.
     *
     * [connect] is restarted every time the event stream drops. A controller rebuilt with it would
     * lose the handle to a tone that is already playing and the deadline that stops it — a phone that
     * screams until somebody reboots it, which is the failure the auto-stop cap exists to prevent.
     */
    private val siren by lazy { SirenController(AndroidSirenDevice(this), HandlerSirenTimer()) }

    /** The one-shot position probe (FR-9). Lazy for the same reason as [alarm]: no `Context` before `onCreate`. */
    private val locationProbe by lazy {
        LocationProbe(AndroidLocationSource(this), { System.currentTimeMillis() })
    }

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        grantOwnPermissions()
        // Then, before anything that can block: the platform kills a service that has not called
        // this within five seconds of being started in the foreground.
        ServiceCompat.startForeground(
            this,
            NOTIFICATION_ID,
            notification(),
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.UPSIDE_DOWN_CAKE) {
                ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE
            } else {
                0
            },
        )

        intent?.let { i ->
            IntentCompat.getParcelableExtra(i, EXTRA_ADMIN_EXTRAS, PersistableBundle::class.java)
                ?.let { pendingExtras = it.toStringMap() }
        }

        if (intent?.action == ACTION_ENFORCE) onEnforcementAlarm()

        if (job?.isActive != true) {
            job = scope.launch { connect() }
        }
        return START_STICKY
    }

    /**
     * The bedtime edge, or the midnight a quota resets at, has arrived.
     *
     * Runs from the cache: nothing about the *policy* changed at this instant, only which side of it
     * the clock is on, so the phone must not need a network to start a bedtime. When the loop is not
     * up yet the start below does a full sync anyway, which enforces and re-books — so the branch
     * logs and does nothing rather than building a second synchronizer.
     */
    private fun onEnforcementAlarm() {
        val synchronizer = live
        if (synchronizer == null) {
            Log.i(TAG, "alarm: fired before the connection was up; the start that follows enforces")
            return
        }
        scope.launch {
            syncLock.withLock {
                withContext(Dispatchers.IO) {
                    when (val result = synchronizer.enforceFromCache()) {
                        is SyncResult.Applied -> {
                            val line = "alarm: ${result.state.suspendReason.ifEmpty { "no reason" }} " +
                                "in effect — ${result.outcome}"
                            if (result.outcome.ok) Log.i(TAG, line) else Log.e(TAG, line)
                            applied(result.state, "alarm")
                        }
                        else -> Log.w(TAG, "alarm: nothing to enforce — $result")
                    }
                }
            }
        }
    }

    override fun onDestroy() {
        // Otherwise the wake-up outlives the service that answers it: it restarts this one, which
        // stops again for whatever reason it stopped for, at every edge for the life of the device.
        runCatching { alarm.schedule(DesiredState()) }
        scope.cancel()
        super.onDestroy()
    }

    private suspend fun connect() {
        val store = EncryptedCredentialStore(this)
        val credentials = obtainCredentials(store)
        if (credentials == null) {
            // Nothing here can be retried by waiting: either the QR was wrong or the server refused
            // it. Stopping is honest — a service that stays up doing nothing looks, from the
            // notification shade, exactly like one that is working.
            stopSelf()
            return
        }

        val api = ApiClient(credentials.serverUrl, token = { store.load()?.deviceToken })
        // Resolved once and shared: the appliers, the command handlers and the inventory reader
        // reach the same managers, and two resolutions would be two answers to "is this app the
        // device owner" that could disagree across a `connect()` that outlives an ownership change.
        val policy = DeviceOwnerPolicy.of(this)
        val reports = reporting(api, policy)
        // Built from the same stores the recovery screen uses, and deliberately not a second set:
        // the screen writes the flag from its own process, and a synchronizer reading a different
        // file would keep enforcing bedtime on a phone somebody had just recovered.
        val recoveryStore = AndroidRecoveryStore(this)
        val recoveryJournal = RecoveryJournal(recoveryStore.journal) { attempt ->
            api.reportRecoveryEvent(
                RecoveryEventRequest(
                    succeeded = attempt.succeeded,
                    occurredAt = OffsetDateTime.ofInstant(
                        Instant.ofEpochMilli(attempt.occurredAtEpochMillis),
                        ZoneId.systemDefault(),
                    ).format(RFC3339),
                )
            )
        }
        val synchronizer = Synchronizer(
            api = api,
            cache = EncryptedPolicyCache(this),
            applier = deviceApplier(policy, credentials.serverUrl, managedAppApplier(api, policy)),
            recovery = RecoveryMode(recoveryStore.mode),
            telemetry = {
                val t = telemetry()
                // Posted from here because this is the one call that happens on every sync and
                // already holds the answer, so the notice follows the appop in both directions
                // without a second poll of its own.
                usageAccessNotice(missing = t.usageAccess == false)
                t
            },
            // The quota has to bite on a phone with no signal, and the only number that is current
            // there is the one this device measured itself. See Synchronizer.localUsedMinutes.
            localUsedMinutes = { input -> reports.usedMinutesToday(input) },
        )
        journal = recoveryJournal
        // Published before the first sync, so an alarm that fires during it waits on `syncLock`
        // rather than finding no synchronizer and logging that it did nothing.
        live = synchronizer
        val commands = policy?.let { commandQueue(api, it, synchronizer, reports) }

        // Once at start, before the stream. A phone that comes back from a reboot must not wait for
        // the server to have something to say before it enforces the policy it already has.
        if (!syncAndDrain(synchronizer, reports, commands, "start")) {
            stopSelf()
            return
        }

        val stream = EventStream(api) { event ->
            // The event is a wake-up and nothing else — see EventStream. Its type is logged so a
            // stream that is delivering the wrong thing is visible, and never read as state.
            if (!syncAndDrain(synchronizer, reports, commands, "wake:${event.type}")) {
                throw StopConnection()
            }
        }
        val installs = registerInstallWatcher(synchronizer, reports, commands)
        val screen = registerScreenWatcher(reports)
        val polling = scope.launch { pollWhileAwake(synchronizer, reports) }
        try {
            stream.run()
        } catch (_: StopConnection) {
            // A refused credential, surfaced from inside the wake handler.
        } finally {
            live = null
            journal = null
            polling.cancel()
            runCatching { unregisterReceiver(installs) }
            runCatching { unregisterReceiver(screen) }
        }
        stream.lastFatal?.let {
            Log.e(TAG, "the server refused this device's credential; stopping: $it")
            runCatching { linkRefusedNotice(refused = true, status = it.status) }
        }
        stopSelf()
    }

    /**
     * Re-applies the policy when a package is installed or replaced (FR-5.4).
     *
     * Registered at runtime rather than in the manifest, and that is the load-bearing half:
     * `ACTION_PACKAGE_ADDED` is an implicit broadcast, and since Android 8 a manifest-declared
     * receiver for one is simply never called. There is no error and no log — a child could install
     * a blocked app and the phone would enforce nothing until the next sync, with every guard in
     * this project green. A context-registered receiver is exempt from that restriction, and this
     * service is the long-lived component that can hold one.
     *
     * `DATA_REMOVED` is not filtered out: an uninstall-then-reinstall arrives as a removal followed
     * by an addition, and a sync on the removal costs one HTTP request while missing the addition
     * costs the whole point of the feature.
     */
    private fun registerInstallWatcher(
        synchronizer: Synchronizer,
        reports: Reporting,
        commands: CommandQueue?,
    ): BroadcastReceiver {
        val receiver = object : BroadcastReceiver() {
            override fun onReceive(context: Context?, intent: Intent?) {
                val pkg = intent?.data?.schemeSpecificPart ?: return
                // Launched rather than run here: onReceive is on the main thread with a ten-second
                // budget, and a sync is a network call. `syncLock` is what keeps this from racing
                // the event stream's own sync — the applier reads the platform, decides, and writes,
                // and two of those interleaved would plan against a half-applied device.
                scope.launch { syncAndDrain(synchronizer, reports, commands, "package:$pkg") }
            }
        }
        val filter = IntentFilter().apply {
            addAction(Intent.ACTION_PACKAGE_ADDED)
            addAction(Intent.ACTION_PACKAGE_REPLACED)
            addDataScheme("package")
        }
        ContextCompat.registerReceiver(this, receiver, filter, ContextCompat.RECEIVER_NOT_EXPORTED)
        return receiver
    }

    /**
     * The screen on/off watcher, which is what makes the measured time *screen* time (FR-3.3).
     *
     * Context-registered for the same reason as the install watcher: `ACTION_SCREEN_ON` and
     * `ACTION_SCREEN_OFF` cannot be declared in a manifest at all, and a receiver that is never
     * called would leave the clock believing the screen never went off — every night would be
     * counted as use, and the quota would be exhausted before the child woke up.
     *
     * The timestamps are `elapsedRealtime`, never the wall clock: it counts through deep sleep and
     * nothing on the device can move it, so a child changing the clock cannot buy screen time.
     */
    private fun registerScreenWatcher(reports: Reporting): BroadcastReceiver {
        val receiver = object : BroadcastReceiver() {
            override fun onReceive(context: Context?, intent: Intent?) {
                val at = SystemClock.elapsedRealtime()
                when (intent?.action) {
                    Intent.ACTION_SCREEN_ON -> reports.onScreenOn(at)
                    Intent.ACTION_SCREEN_OFF -> {
                        reports.onScreenOff(at)
                        // Measured here rather than only at the next poll: the window that just
                        // ended is the one the child spent, and a phone that is put down for the
                        // night may not sync again before midnight moves it onto another day.
                        scope.launch { withContext(Dispatchers.IO) { report(reports, "screen-off") } }
                    }
                }
            }
        }
        val filter = IntentFilter().apply {
            addAction(Intent.ACTION_SCREEN_ON)
            addAction(Intent.ACTION_SCREEN_OFF)
        }
        ContextCompat.registerReceiver(this, receiver, filter, ContextCompat.RECEIVER_NOT_EXPORTED)
        return receiver
    }

    /**
     * Measures and re-enforces while the screen is on, and does nothing at all while it is off.
     *
     * The quota is the reason this loop exists. Nothing else wakes the device between a parent's
     * changes, so without it a child could pass the daily limit at 16:00 and keep going until the
     * server happened to send an event — and offline, until the next morning. Re-enforcing runs from
     * the *cache*, so it costs no network: the policy has not changed, only the minutes have.
     *
     * While the screen is off there is nothing to measure and nothing to enforce, so the loop waits
     * on [Reporting.awaitScreenOn] rather than ticking and finding zero (NFR-10).
     */
    private suspend fun pollWhileAwake(synchronizer: Synchronizer, reports: Reporting) {
        while (true) {
            reports.awaitScreenOn()
            delay(POLL_INTERVAL_MILLIS)
            if (!reports.screenIsOn()) continue
            syncLock.withLock {
                withContext(Dispatchers.IO) {
                    report(reports, "poll")
                    val result = synchronizer.enforceFromCache()
                    if (result is SyncResult.Applied) {
                        if (result.state.suspendReason.isNotEmpty()) {
                            Log.i(TAG, "poll: ${result.state.suspendReason} in effect — ${result.outcome}")
                        }
                        // Re-booked from here too: the quota's midnight edge only exists while a
                        // daily limit is set, so a policy that gains one between two server events
                        // would otherwise have no wake-up until the next one arrived.
                        book(result.state, "poll")
                    }
                }
            }
        }
    }

    /**
     * Measures the window that just ended, delivers what is outstanding, and says what happened.
     *
     * Every branch logs, including the ones that did nothing, because the failure this whole path
     * exists to avoid is silent: usage that reads zero shows the parent a child who was off their
     * phone all day and makes the daily limit unreachable, and it looks exactly like a well-behaved
     * child. "not measured" has to be visible as itself.
     */
    private fun report(reports: Reporting, why: String) {
        val outcome = reports.measureAndDeliver()
        when (val tick = outcome.tick) {
            is UsageTick.Measured ->
                Log.i(TAG, "$why: measured ${tick.byDay.keys.joinToString()}")
            is UsageTick.NotMeasured ->
                Log.w(TAG, "$why: screen time NOT MEASURED — ${tick.reason}")
            UsageTick.Idle -> Unit
        }
        if (!outcome.flush.ok) {
            Log.w(TAG, "$why: ${outcome.flush}")
        } else if (outcome.flush.sent.isNotEmpty()) {
            Log.i(TAG, "$why: ${outcome.flush}")
        }
        when (val inventory = outcome.inventory) {
            is InventoryResult.Sent ->
                Log.i(TAG, "$why: inventory sent=${inventory.sent} stored=${inventory.stored}")
            is InventoryResult.NotMeasured ->
                Log.w(TAG, "$why: inventory NOT MEASURED — ${inventory.reason}")
            is InventoryResult.Failed ->
                Log.w(TAG, "$why: inventory not delivered: ${inventory.cause.message}")
            InventoryResult.Unchanged -> Unit
        }
    }

    /**
     * Syncs, then drains whatever commands the server said were waiting.
     *
     * **The drain is deliberately outside [syncLock].** Four of the eight command handlers re-sync —
     * `SYNC_POLICY` and the three that only flip a server-side setting — so a drain that ran with
     * the lock held would deadlock on the commonest command in the product, and `Mutex` is not
     * reentrant. Nothing is lost by letting go: the drain talks to the server, and each handler that
     * needs to touch the device takes the lock again through [runSync].
     *
     * @return false when the credential is gone and the loop must not continue.
     */
    private suspend fun syncAndDrain(
        synchronizer: Synchronizer,
        reports: Reporting,
        commands: CommandQueue?,
        why: String,
    ): Boolean {
        val tick = runSync(synchronizer, reports, why) ?: return false
        if (tick.pending > 0) drain(commands, tick.pending, why)
        return true
    }

    /**
     * @return what the sync did, or null when the credential is gone and the loop must not continue.
     */
    private suspend fun runSync(
        synchronizer: Synchronizer,
        reports: Reporting,
        why: String,
    ): SyncTick? = syncLock.withLock {
        val result = withContext(Dispatchers.IO) { synchronizer.sync() }
        when (result) {
            is SyncResult.Applied -> {
                val line = "$why: applied v${result.state.policyVersion} from ${result.source} — " +
                    "${result.outcome}, pending=${result.pendingCommands}"
                if (result.outcome.ok) Log.i(TAG, line) else Log.e(TAG, line)
                applied(result.state, why)
                // Only on SERVER: a policy that came from the cache means the fetch failed, and
                // reporting over a link that has just refused a request produces a log full of
                // failures rather than a delivery. Nothing is lost by waiting — the day totals are
                // cumulative and stay pending until they land.
                if (result.source == PolicySource.SERVER) {
                    withContext(Dispatchers.IO) {
                        // The server answered, so whatever this phone was told about being unlinked
                        // is over (FR-1.8). Cleared here and nowhere else: only the server can end
                        // a condition that is entirely about what the server thinks.
                        linkRefusedNotice(refused = false)
                        report(reports, why)
                        // Only here, and for the same reason: this is the one moment the device
                        // knows the control plane answered. A recovery entered offline is delivered
                        // by the first sync that gets through, however many days later that is.
                        flushRecovery(why)
                    }
                }
                return SyncTick(
                    pending = result.pendingCommands,
                    // A policy that came from the *cache* is one the server never answered for, and
                    // a `SYNC_POLICY` command must not report "re-fetched" for it. An applier that
                    // reported problems is the same shape of half-success.
                    problem = when {
                        result.source != PolicySource.SERVER ->
                            "the server could not be reached; the cached policy is in effect"
                        !result.outcome.ok -> "applied with problems: ${result.outcome}"
                        else -> null
                    },
                )
            }
            is SyncResult.Refused -> {
                Log.e(TAG, "$why: the server refused this device's credential: ${result.cause}")
                withContext(Dispatchers.IO) { linkRefusedNotice(refused = true, status = result.cause.status) }
                return null
            }
            // Both remaining cases mean nothing was enforced and nothing here learned whether a
            // command is waiting. `pending = 0` is the count this sync *learned* about — the next
            // one asks again — and never a claim that the queue is empty.
            is SyncResult.Deferred -> {
                val problem = "nothing to enforce yet — ${result.cause.message}"
                Log.w(TAG, "$why: $problem")
                return SyncTick(pending = 0, problem = problem)
            }
            is SyncResult.Rejected -> {
                val problem = "the policy could not be evaluated: ${result.cause.message}"
                Log.e(TAG, "$why: $problem")
                return SyncTick(pending = 0, problem = problem)
            }
            is SyncResult.Released -> {
                // WARN and not ERROR: an unmanaged phone is the *intended* outcome here, and a
                // parent who typed the code is watching for it. What must stay visible is that it is
                // still unmanaged, on every sync, until the server is reached — see RecoveryMode.
                val problem = "released by a recovery code since ${result.since ?: "an unknown time"}" +
                    "; enforcing nothing until the server is reached — ${result.outcome}"
                Log.w(TAG, "$why: $problem")
                return SyncTick(pending = 0, problem = problem)
            }
        }
    }

    /**
     * Delivers the recovery attempts this device has recorded (FR-12.5).
     *
     * Every branch says something, including the empty one by staying silent: a queue that is not
     * draining is the failure worth seeing, and it is invisible from the console — which shows
     * exactly the events that *did* arrive.
     */
    private fun flushRecovery(why: String) {
        val pending = journal ?: return
        val result = pending.flush()
        if (result.sent.isNotEmpty()) Log.i(TAG, "$why: recovery events sent=${result.sent.size}")
        if (result.failed.isNotEmpty()) {
            Log.w(TAG, "$why: ${result.failed.size} recovery event(s) not delivered: ${result.failed.values}")
        }
        if (result.dropped.isNotEmpty()) {
            // ERROR, unlike a retry: these are gone. A parent looking at the console will see fewer
            // attempts than the child made, and this line is the only record that says so.
            Log.e(TAG, "$why: ${result.dropped.size} recovery event(s) REFUSED and dropped: ${result.dropped.values}")
        }
    }

    /**
     * Fetches and runs the queue (FR-9).
     *
     * A device with no command executor is one that is not the device owner: the handlers reach the
     * keyguard and the policy managers, and building them from a null policy would produce commands
     * that acknowledge success having done nothing. Logged loudly instead — the same fact the
     * applier is already reporting, at the moment it costs a parent a command they pressed.
     */
    private suspend fun drain(commands: CommandQueue?, pending: Int, why: String) {
        if (commands == null) {
            Log.e(TAG, "$why: $pending command(s) waiting and this device is not the owner; none can run")
            return
        }
        val report = withContext(Dispatchers.IO) {
            try {
                commands.drain()
            } catch (e: Exception) {
                // Only the fetch throws — see CommandQueue. Nothing was handed over, so the rows are
                // still QUEUED and the next sync collects them.
                Log.w(TAG, "$why: $pending command(s) waiting, not fetched: ${e.message ?: e.javaClass.simpleName}")
                null
            }
        } ?: return
        if (report.ok) Log.i(TAG, "$why: $report") else Log.e(TAG, "$why: $report")
    }

    /**
     * The usage tracker, the day-total reporter and the inventory reporter, wired to this device.
     *
     * The screen clock starts from the screen's *current* state rather than from `true`: this
     * service is started at boot and after a kill, and assuming the screen is on would credit the
     * whole of the first window to whatever the child had open before the phone was put down.
     */
    private fun reporting(api: ApiClient, policy: DeviceOwnerPolicy?): Reporting {
        val ledger = UsageLedger(EncryptedUsageStore(this))
        val power = getSystemService(PowerManager::class.java)
        val zone = PolicyZone()
        val tracker = UsageTracker(
            reader = UsageStatsForegroundReader(this),
            ledger = ledger,
            screen = ScreenOnClock(
                screenOn = power?.isInteractive ?: false,
                startMillis = SystemClock.elapsedRealtime(),
            ),
            zone = { zone.current },
            wallClock = { System.currentTimeMillis() },
            monotonicClock = { SystemClock.elapsedRealtime() },
        )
        val digests = encryptedPreferences(this, INVENTORY_FILE)
        return Reporting(
            tracker = tracker,
            ledger = ledger,
            zone = zone,
            usage = UsageReporter(ledger) { day, samples ->
                api.reportUsage(UsageRequest(day, samples))
            },
            inventory = InventoryReporter(
                reader = PlatformInstalledAppReader(this, restraint = policy?.apps?.gateway),
                send = { apps ->
                    api.reportInventory(
                        InventoryRequest(
                            apps.map {
                                InventoryApp(
                                    it.packageName, it.label, it.systemApp, it.hidden, it.suspended,
                                )
                            }
                        )
                    )
                },
                lastDigest = { digests.getString(KEY_INVENTORY_DIGEST, "").orEmpty() },
                recordDigest = {
                    digests.edit().putString(KEY_INVENTORY_DIGEST, it).commit()
                },
            ),
        )
    }

    /**
     * Records what is now in effect: one log line, and the wake-up for when it next changes.
     *
     * Both halves belong together. The state and its `next_change_at` are computed in the same pass,
     * and a state applied without re-booking is a device enforcing today's answer forever — which is
     * exactly what the log line would show, correctly, at the moment it stopped being true.
     */
    private fun applied(state: DesiredState, why: String) {
        describe(state)
        val decision = book(state, why)
        if (decision is AlarmDecision.Scheduled && decision.exact) Log.i(TAG, "$why: $decision")
        if (decision is AlarmDecision.Cancelled) Log.i(TAG, "$why: $decision")
    }

    /**
     * Books the wake-up, and logs it only when it is not the ordinary case.
     *
     * The poll runs every five minutes with the screen on, so an INFO line per booking would be the
     * loudest thing in the log and would bury the two that matter: a wake-up the platform may delay,
     * and one that was not booked at all.
     */
    private fun book(state: DesiredState, why: String): AlarmDecision {
        val decision = alarm.schedule(state)
        when (decision) {
            is AlarmDecision.Scheduled -> if (!decision.exact) Log.w(TAG, "$why: $decision")
            is AlarmDecision.Unreadable -> Log.e(TAG, "$why: $decision")
            is AlarmDecision.Refused -> Log.e(TAG, "$why: $decision")
            AlarmDecision.Cancelled -> Unit
        }
        return decision
    }

    /** One line naming what is in effect, so a bug report from a real phone says what it enforced. */
    private fun describe(state: DesiredState) {
        Log.i(
            TAG,
            "in effect: reason=${state.suspendReason.ifEmpty { "none" }} " +
                "suspended=${state.suspendedPackages.size} hidden=${state.hiddenPackages.size} " +
                "domains=${state.blockedDomains.size} dns=${state.privateDnsHost.ifEmpty { "none" }} " +
                "restrictions=${state.userRestrictions.size} next=${state.nextChangeAt.ifEmpty { "never" }}",
        )
    }

    /**
     * Enrolls if this device has not, retrying only what waiting can fix.
     *
     * @return the credential, or null when there is nothing to wait for: no extras and no stored
     * credential (a device that was never provisioned by us), a QR that cannot produce an
     * enrollment, or a token the server refused.
     */
    private suspend fun obtainCredentials(store: CredentialStore): Credentials? {
        val backoff = Backoff()
        while (true) {
            withContext(Dispatchers.IO) { store.load() }?.let { return it }

            val extras = pendingExtras
            if (extras.isEmpty()) {
                Log.w(TAG, "not enrolled and no provisioning extras: nothing to enroll with")
                return null
            }

            val result = withContext(Dispatchers.IO) {
                Enroller(
                    store = store,
                    // Debug builds only, and even then only for loopback and the emulator's host
                    // alias — see Enroller.CLEARTEXT_HOSTS. A release build refuses http:// outright,
                    // which is also what the network security config enforces one layer down.
                    cleartextAllowed = BuildConfig.DEBUG,
                ).enroll(extras, deviceFacts())
            }
            when (result) {
                is EnrollResult.Enrolled -> {
                    Log.i(TAG, "enrolled as device ${result.credentials.deviceId}")
                    return result.credentials
                }
                is EnrollResult.AlreadyEnrolled -> return result.credentials
                is EnrollResult.Misprovisioned -> {
                    Log.e(TAG, "cannot enroll: ${result.reason}")
                    return null
                }
                is EnrollResult.Refused -> {
                    Log.e(TAG, "the server refused this enrollment: ${result.cause}")
                    return null
                }
                is EnrollResult.Deferred -> {
                    Log.w(TAG, "enrollment deferred, retrying: ${result.cause.message}")
                    delay(backoff.nextDelayMillis())
                }
            }
        }
    }

    /**
     * What this hardware calls itself, and its own dialer/launcher/IME.
     *
     * The critical packages are read from the platform rather than guessed, because the built-in
     * list in the engine cannot know an OEM's dialer — and the one thing bedtime must never suspend
     * is the child's ability to call for help (FR-5.5).
     */
    private fun deviceFacts(): DeviceFacts = androidDeviceFacts(this)

    /**
     * The command queue, wired to this device (FR-9).
     *
     * The siren and the location probe are service fields rather than locals: [connect] is restarted
     * every time the event stream drops, and a controller rebuilt with it would lose the handle to a
     * tone that is still playing — a phone that screams until somebody reboots it.
     */
    /**
     * The FR-16 pass, wired to this device's platform and this device's credential.
     *
     * Null when this app is not the device owner — there is nothing it could install, and an
     * applier that reported a problem per declared app on a phone it does not manage would bury the
     * one problem that matters, which [NoDeviceOwnerApplier] already states once.
     */
    private fun managedAppApplier(api: ApiClient, policy: DeviceOwnerPolicy?): StateApplier? {
        if (policy == null) return null
        val installer = AndroidInstaller(this)
        return ManagedAppApplier(
            hardening = policy.hardening,
            installedVersion = { pkg -> installer.installed(pkg)?.versionCode },
            installedByThisApp = installer::installedByThisApp,
            stage = { app ->
                AppUpdater(
                    info = {
                        ApkInfo(
                            packageName = app.packageName,
                            url = app.url,
                            packageChecksum = app.checksum,
                            size = app.size,
                        )
                    },
                    // Authenticated, unlike the self-update download: the managed-app route is
                    // device-authenticated, and `openDownload` is what refuses to put this
                    // device's bearer on a URL that is not this deployment's.
                    open = { url -> api.openDownload(url) },
                    staging = { installer.staging(app.packageName) },
                    identify = installer::identify,
                    installed = installer::installed,
                    // Throwing is the return channel here, and deliberately: `commit` is
                    // `() -> Unit` because the self-update path hands it to the command
                    // acknowledgement as an `after` hook, and a platform failure on THIS path has
                    // to reach the applier's problem map. ManagedAppApplier catches it and records
                    // the text against the package.
                    install = { file, pkg ->
                        installer.installAwaiting(file, pkg)?.let { throw IOException(it) }
                    },
                    log = { Log.i(TAG, "managed app: $it") },
                ).update()
            },
            uninstall = installer::uninstallAwaiting,
            log = { Log.i(TAG, it) },
        )
    }

    private fun commandQueue(
        api: ApiClient,
        policy: DeviceOwnerPolicy,
        synchronizer: Synchronizer,
        reports: Reporting,
    ): CommandQueue {
        val handlers = CommandHandlers(
            lock = policy.lock,
            siren = siren,
            location = locationProbe,
            reportLocation = api::reportLocation,
            resync = {
                // `runBlocking` on an IO thread, and deliberately: a handler is a plain function so
                // that it can be unit-tested without a coroutine, and this is the one call that has
                // to cross back. It cannot deadlock — `syncLock` is *not* held here, which is the
                // whole reason the drain runs outside it (see syncAndDrain).
                runBlocking {
                    when (val tick = runSync(synchronizer, reports, "command")) {
                        null -> "the server refused this device's credential"
                        else -> tick.problem
                    }
                }
            },
            update = {
                val installer = AndroidInstaller(this)
                AppUpdater(
                    info = {
                        val r = api.apkInfo()
                        // The package is stated here rather than taken from the response: this
                        // command replaces THIS app, and an apk-info naming something else must be
                        // refused rather than installed alongside. The server never sends it.
                        ApkInfo(
                            packageName = packageName,
                            url = r.url,
                            packageChecksum = r.packageChecksum,
                            size = r.size,
                        )
                    },
                    // The download is deliberately NOT an ApiClient call: /dpc.apk is
                    // unauthenticated by design — a factory-reset phone fetches it during
                    // provisioning with no credential — and sending this device's bearer token to
                    // an absolute URL the server named is how a token leaves the deployment it
                    // belongs to. The URL is used, the credential is not.
                    open = { url -> URL(url).openStream() },
                    staging = { installer.staging(packageName) },
                    identify = installer::identify,
                    installed = installer::installed,
                    // Fire and forget, and it must stay that way: this commit kills the process, so
                    // there is no status to wait for and nothing left to report it to.
                    install = installer::install,
                    log = { Log.i(TAG, "update: $it") },
                ).update()
            },
        ).asMap()
        return CommandQueue(
            fetch = { api.commands() },
            executor = CommandExecutor(
                handlers = handlers,
                ack = { id, ok, result, error -> api.ackCommand(id, AckRequest(ok, result, error)) },
                log = { Log.w(TAG, "command: $it") },
            ),
        )
    }

    /**
     * This app's own version, from the package manager.
     *
     * Empty and zero when the package manager cannot answer about the package it is running — which
     * should not happen and is reported as "not measured" rather than as version 0, because the
     * server stores a zero as "never reported" and a fabricated one would read as a real build.
     */
    private val selfVersion: Pair<String, Long>
        get() = try {
            val info = packageManager.getPackageInfo(packageName, 0)
            info.versionName.orEmpty() to info.longVersionCode
        } catch (e: Exception) {
            Log.w(TAG, "cannot read this app's own version: ${e.message ?: e.javaClass.simpleName}")
            "" to 0L
        }

    private fun telemetry(): DeviceTelemetry {
        val battery = registerReceiver(null, IntentFilter(Intent.ACTION_BATTERY_CHANGED))
        val level = battery?.getIntExtra(BatteryManager.EXTRA_LEVEL, -1) ?: -1
        val scale = battery?.getIntExtra(BatteryManager.EXTRA_SCALE, -1) ?: -1
        val status = battery?.getIntExtra(BatteryManager.EXTRA_STATUS, -1) ?: -1

        val power = getSystemService(PowerManager::class.java)
        val connectivity = getSystemService(ConnectivityManager::class.java)
        val capabilities = connectivity?.activeNetwork?.let { connectivity.getNetworkCapabilities(it) }

        return DeviceTelemetry(
            // Null, not a percentage, when the broadcast did not carry the numbers. A fabricated
            // "0%" would show the parent a phone about to die.
            batteryLevel = if (level >= 0 && scale > 0) level * 100 / scale else null,
            charging = if (status >= 0) {
                status == BatteryManager.BATTERY_STATUS_CHARGING ||
                    status == BatteryManager.BATTERY_STATUS_FULL
            } else {
                null
            },
            screenOn = power?.isInteractive,
            appVersionName = selfVersion.first,
            appVersionCode = selfVersion.second,
            // Read on every heartbeat, not once at start: the appop is granted by hand in Settings
            // and can be revoked the same way, and the console must follow it in both directions.
            usageAccess = UsageAccess.granted(this),
            connectivity = when {
                capabilities == null -> "none"
                capabilities.hasTransport(NetworkCapabilities.TRANSPORT_WIFI) -> "wifi"
                capabilities.hasTransport(NetworkCapabilities.TRANSPORT_CELLULAR) -> "cellular"
                capabilities.hasTransport(NetworkCapabilities.TRANSPORT_ETHERNET) -> "ethernet"
                else -> "other"
            },
        )
    }

    /**
     * Grants this app the runtime permissions it needs, which only a device owner may do.
     *
     * There is nobody to ask. This app has no UI a person opens, and the phone belongs to a child who
     * must not be able to answer a permission dialog — so a permission that is not granted here is
     * one that stays denied forever, and every feature behind it fails silently:
     *
     * - `POST_NOTIFICATIONS` (Android 13+): a foreground service whose notification is suppressed
     *   still runs, invisibly. The parent loses the one on-device sign the DPC is alive, and the
     *   child loses the disclosure that the phone is managed — which this project treats as a
     *   requirement, not a courtesy.
     * - the three location permissions: "locate now" returns "no position" on a phone whose GPS is
     *   working perfectly.
     * - `ACCESS_LOCAL_NETWORK` (Android 37+): every packet this app sends to a control plane on the
     *   family's own network is dropped, and dropped rather than refused — the connection times out
     *   and the log says only that the server did not answer. This is the one the platform refuses
     *   to hand over: the call below reports success and the permission stays denied, which is why
     *   the loop reads the result back. See the manifest for the measurement and the way out.
     *
     * Failure is logged per permission and never fatal. On a device this app does not own every grant
     * is refused, and that is already reported far more loudly by the applier.
     */
    private fun grantOwnPermissions() {
        val dpm = getSystemService(DevicePolicyManager::class.java) ?: return
        if (!dpm.isDeviceOwnerApp(packageName)) return

        // What each one costs if it is refused, in the words the log should carry. Only runtime
        // permissions belong here: `setPermissionGrantState` cannot set an appop, so
        // PACKAGE_USAGE_STATS and SCHEDULE_EXACT_ALARM stay manual steps in DEPLOYMENT.md and are
        // reported as "not measured" by the code that needs them.
        val needed = buildList {
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
                add(Manifest.permission.POST_NOTIFICATIONS to "this service runs unseen")
            }
            add(Manifest.permission.ACCESS_FINE_LOCATION to "'locate now' cannot get a position")
            add(Manifest.permission.ACCESS_COARSE_LOCATION to "'locate now' loses its approximate fallback")
            // Last, and on purpose: the platform refuses BACKGROUND unless a foreground location
            // permission is already held, so granting it first would fail on a fresh device and look
            // like a device that is not the owner.
            add(Manifest.permission.ACCESS_BACKGROUND_LOCATION to "'locate now' works only while this app is in front")
            // Guarded by version because the permission does not exist before 37: asking to grant a
            // name the platform has never heard of fails, and it would fail on every start of every
            // older phone, logging a cost that phone does not pay.
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.CINNAMON_BUN) {
                add(Manifest.permission.ACCESS_LOCAL_NETWORK to "a control plane on the family's own network is unreachable")
            }
        }

        for ((permission, cost) in needed) {
            val accepted = runCatching {
                dpm.setPermissionGrantState(
                    AdminReceiver.component(this),
                    packageName,
                    permission,
                    DevicePolicyManager.PERMISSION_GRANT_STATE_GRANTED,
                )
            }.getOrElse {
                Log.w(TAG, "could not grant $permission: ${it.message}")
                false
            }
            // Read back rather than believe the return value. `setPermissionGrantState` answers "the
            // policy was recorded", which is not "the app holds it" — measured on Android 37, it
            // returned true for ACCESS_LOCAL_NETWORK while checkSelfPermission stayed DENIED. A
            // grant that is believed rather than read is a permission this app only thinks it has,
            // and the feature behind it then fails with a symptom that names something else.
            val held = checkSelfPermission(permission) == PackageManager.PERMISSION_GRANTED
            // Logged every time it is not held rather than once: this runs on every start, and a
            // permission that was revoked by hand between two starts is exactly what this must show.
            if (!held) Log.w(TAG, "$permission is not held (policy accepted=$accepted) — $cost")
        }
    }

    /**
     * The one setup step no code on this phone can perform (FR-3.6).
     *
     * `PACKAGE_USAGE_STATS` is an appop, not a runtime permission: `setPermissionGrantState`
     * returns false for it and no device-owner API can grant it, so it is turned on by hand in
     * Settings → Apps → Special app access → Usage access. Until it is, every usage query returns
     * nothing, every package reads zero minutes and no daily limit is ever reached — and a parent
     * looking at the console cannot tell that from a child who was off their phone.
     *
     * So the phone says so, and says it where a person will see it, with the Settings screen one
     * tap away. Not ongoing and not auto-cancelling: dismissing it must be possible, and the next
     * sync puts it back while the grant is still missing. It is cancelled the moment the grant
     * arrives, which is what makes it a signal rather than furniture.
     */
    private fun usageAccessNotice(missing: Boolean) {
        val manager = getSystemService(NotificationManager::class.java) ?: return
        if (!missing) {
            manager.cancel(SETUP_NOTIFICATION_ID)
            return
        }
        manager.createNotificationChannel(
            NotificationChannel(
                SETUP_CHANNEL,
                getString(R.string.setup_channel),
                NotificationManager.IMPORTANCE_DEFAULT,
            )
        )
        val text = getString(R.string.usage_access_text)
        val builder = NotificationCompat.Builder(this, SETUP_CHANNEL)
            .setContentTitle(getString(R.string.usage_access_title))
            .setContentText(text)
            .setStyle(NotificationCompat.BigTextStyle().bigText(text))
            .setSmallIcon(android.R.drawable.ic_dialog_info)
        val settings = usageAccessIntent()
        if (settings != null) {
            builder.setContentIntent(
                PendingIntent.getActivity(this, 1, settings, PendingIntent.FLAG_IMMUTABLE)
            )
        } else {
            Log.w(TAG, "this build has no usage-access settings screen to link to")
        }
        manager.notify(SETUP_NOTIFICATION_ID, builder.build())
    }

    /**
     * Records that the server has stopped accepting this phone's credential, and tells the parent
     * where the way back is (FR-1.8).
     *
     * The notification is the entire point of the flag. Before this existed the phone went quiet:
     * `ConnectionService` logged the refusal and stopped, the console showed a device that was
     * simply offline, and nothing on the handset said anything at all. The first real phone this
     * project enrolled sat in exactly that state, and the only remedy anybody could find was a
     * factory reset.
     *
     * Not `IMPORTANCE_HIGH`: it is already the only notification this app raises that a person must
     * act on, and heads-up on a child's phone every time it is drawn is a notification a child
     * turns off. `setOngoing` for the same reason the state is persisted — this does not resolve on
     * its own, and a swipe should not be mistaken for a fix.
     *
     * @param status the HTTP status the server answered, when there is one. Only 401 sets the flag;
     *   see [LinkRefused.recordRefusal]. Any other refusal still stops the loop and is still
     *   logged, it just does not claim the credential is the problem.
     */
    private fun linkRefusedNotice(refused: Boolean, status: Int = 0) {
        val link = LinkRefused(AndroidRecoveryStore(this).link)
        if (refused) link.recordRefusal(status) else link.clear()

        val manager = getSystemService(NotificationManager::class.java) ?: return
        if (!link.refused()) {
            manager.cancel(UNLINKED_NOTIFICATION_ID)
            return
        }
        manager.createNotificationChannel(
            NotificationChannel(
                SETUP_CHANNEL,
                getString(R.string.setup_channel),
                NotificationManager.IMPORTANCE_DEFAULT,
            )
        )
        val text = getString(R.string.unlinked_text)
        manager.notify(
            UNLINKED_NOTIFICATION_ID,
            NotificationCompat.Builder(this, SETUP_CHANNEL)
                .setContentTitle(getString(R.string.unlinked_title))
                .setContentText(text)
                .setStyle(NotificationCompat.BigTextStyle().bigText(text))
                .setSmallIcon(android.R.drawable.ic_dialog_alert)
                .setOngoing(true)
                // The same screen the launcher entry opens, and for the same reason it is a
                // launcher entry at all: this notification exists only while the service has just
                // run, and the situation it announces is one where the service stops.
                .setContentIntent(
                    PendingIntent.getActivity(
                        this,
                        2,
                        Intent(this, RecoveryActivity::class.java)
                            .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK),
                        PendingIntent.FLAG_IMMUTABLE,
                    )
                )
                .build(),
        )
    }

    /**
     * The deepest link to Usage access that this phone will actually accept, or null if it offers
     * none.
     *
     * The plain `ACTION_USAGE_ACCESS_SETTINGS` drops a parent at the top of a list that on a Samsung
     * runs to several hundred apps, sorted in a way nobody can predict, with FamilyGuard somewhere in
     * it. Two extras change that, and they are what every app that asks for an appop uses:
     *
     *  - `:settings:fragment_args_key` names the preference row to scroll to and **flash** — the
     *    highlight animation AOSP's `SettingsActivity` plays on arrival, which is the whole point.
     *    The row is keyed by package name on this screen.
     *  - `:settings:show_fragment_args` carries the same key in the bundle the fragment is handed,
     *    because the two are read at different points and builds differ in which one they honour.
     *    Sending both is the documented-by-practice way to hit both.
     *
     * They are string literals rather than `Settings.EXTRA_FRAGMENT_ARG_KEY` because that constant
     * is hidden: it is not on the API-29 floor this app builds against, and referencing it would not
     * compile. The literals are the stable half of that contract.
     *
     * The `package:` data URI is the third hint, and on several OEM builds it is the one that opens
     * the per-app screen outright rather than the list.
     *
     * **Resolved rather than assumed, and in two stages.** Adding data to an intent narrows which
     * filters match, so the decorated intent can resolve to nothing on a build whose usage-access
     * filter declares no data scheme — and an unresolvable content intent is a tap that throws in
     * the system UI, which reads to a parent as an app that is broken rather than as a phone that
     * cannot offer the setting. So: try the specific one, fall back to the bare one, and say in the
     * log which was used, because "the highlight did not flash" and "the deep link was not taken"
     * are otherwise the same observation.
     */
    private fun usageAccessIntent(): Intent? {
        val bare = Intent(Settings.ACTION_USAGE_ACCESS_SETTINGS).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
        val highlighted = Intent(bare)
            .setData(Uri.fromParts("package", packageName, null))
            .putExtra(SETTINGS_FRAGMENT_ARG_KEY, packageName)
            .putExtra(
                SETTINGS_SHOW_FRAGMENT_ARGS,
                // The platform Bundle rather than the androidx helper, which is deprecated for
                // losing type safety -- and this build treats a warning as an error.
                Bundle().apply { putString(SETTINGS_FRAGMENT_ARG_KEY, packageName) },
            )
        if (highlighted.resolveActivity(packageManager) != null) return highlighted
        Log.i(TAG, "this build does not accept the highlighted usage-access link; using the plain one")
        return bare.takeIf { it.resolveActivity(packageManager) != null }
    }

    private fun notification(): Notification {
        val manager = getSystemService(NotificationManager::class.java)
        // Low importance: it must be visible — the platform requires a foreground service to say so
        // — but a phone that pings every time it reconnects is a phone whose owner turns the
        // notification off.
        manager?.createNotificationChannel(
            NotificationChannel(
                CHANNEL,
                getString(R.string.connection_channel),
                NotificationManager.IMPORTANCE_LOW,
            )
        )
        return NotificationCompat.Builder(this, CHANNEL)
            .setContentTitle(getString(R.string.app_name))
            .setContentText(getString(R.string.connection_running))
            .setSmallIcon(android.R.drawable.ic_lock_idle_lock)
            .setOngoing(true)
            // Tapping the disclosure opens the escape hatch (FR-12). The launcher entry is the
            // route that survives this service being dead, so this is a shortcut rather than the
            // way in — but it is the one a parent finds while looking at the thing that is managing
            // the phone, which is where they look first.
            //
            // IMMUTABLE because nothing may fill in fields the activity would then read; it reads
            // no extras at all, and an immutable intent is what keeps that true of any caller who
            // gets hold of this. `getActivity` and not `getBroadcast`: this must open a screen a
            // person interacts with, never perform an action from a tap.
            .setContentIntent(
                PendingIntent.getActivity(
                    this,
                    0,
                    Intent(this, RecoveryActivity::class.java),
                    PendingIntent.FLAG_IMMUTABLE,
                )
            )
            .build()
    }

    /** Ends [EventStream.run] from inside the wake handler; see [connect]. */
    private class StopConnection : RuntimeException()

    companion object {
        private const val TAG = "FamilyGuard/Connection"
        private const val CHANNEL = "family-guard-connection"
        private const val NOTIFICATION_ID = 1

        /** A separate channel so a parent can silence the running notice and still be told this. */
        /**
         * The two extras AOSP's Settings reads to scroll to a preference row and flash it. Hidden
         * platform constants (`Settings.EXTRA_FRAGMENT_ARG_KEY` and
         * `SettingsActivity.EXTRA_SHOW_FRAGMENT_ARGUMENTS`), so the literals are written out — they
         * are not on the API-29 floor and would not compile by name. An unknown extra is ignored,
         * so a build that reads neither is no worse off than one that was never sent them.
         */
        private const val SETTINGS_FRAGMENT_ARG_KEY = ":settings:fragment_args_key"
        private const val SETTINGS_SHOW_FRAGMENT_ARGS = ":settings:show_fragment_args"

        private const val SETUP_CHANNEL = "family-guard-setup"
        private const val UNLINKED_NOTIFICATION_ID = 3
        private const val SETUP_NOTIFICATION_ID = 2

        /**
         * The inventory digest lives in its own preferences file, so that clearing the policy cache
         * or the credential cannot take it — and, more importantly, so that clearing *it* cannot
         * take the day totals. Losing it costs one redundant inventory POST; losing them costs a
         * child's spent quota.
         */
        private const val INVENTORY_FILE = "family-guard-inventory"
        private const val KEY_INVENTORY_DIGEST = "digest"

        /**
         * The instant format the backend parses, spelled out for the same reason [Synchronizer]
         * spells its own out: `OffsetDateTime.toString()` drops the seconds field when it is zero,
         * which is legal RFC 3339 and a shape this app's other timestamps never have.
         */
        private val RFC3339: DateTimeFormatter =
            DateTimeFormatter.ofPattern("yyyy-MM-dd'T'HH:mm:ssXXX")

        /**
         * How often the device re-measures and re-enforces while the screen is on.
         *
         * Five minutes is the resolution of the daily limit: a child can overrun it by at most this
         * long before the phone suspends. Shorter costs battery for no visible gain (the console
         * shows minutes, and the parent is not watching a stopwatch); much longer turns "you have 10
         * minutes left" into a number that was true a while ago.
         */
        private const val POLL_INTERVAL_MILLIS = 5 * 60 * 1000L

        /** The provisioning admin extras, as the platform delivered them. */
        const val EXTRA_ADMIN_EXTRAS = "io.github.helios57.familyguard.ADMIN_EXTRAS"

        /**
         * The enforcement wake-up, sent by [AlarmManagerPlatform] and by nothing else.
         *
         * The service is not exported, so this is reachable only from inside this app — which is why
         * receiving it is allowed to act rather than having to authenticate the sender first.
         */
        const val ACTION_ENFORCE = "io.github.helios57.familyguard.ENFORCE_NOW"

        /**
         * Starts the service, carrying the provisioning extras when there are any.
         *
         * `startForegroundService` rather than `startService`: this is called from a boot receiver
         * and from a provisioning callback, both of which run while the app is in the background,
         * where the plain form throws.
         */
        fun start(context: Context, adminExtras: PersistableBundle?) {
            val intent = Intent(context, ConnectionService::class.java)
            adminExtras?.let { intent.putExtra(EXTRA_ADMIN_EXTRAS, it) }
            ContextCompat.startForegroundService(context, intent)
        }
    }
}

/**
 * The provisioning extras as a plain map, which is the only shape [Enroller] knows.
 *
 * `getString` and not `get`: an extra written as an int by a control plane that is not ours would
 * otherwise arrive as "42" through `toString()` and be sent to the server as an enrollment token.
 */
private fun PersistableBundle.toStringMap(): Map<String, String?> =
    keySet().associateWith { getString(it) }

/**
 * The policy's timezone, last seen, shared between the sync path and the measuring path.
 *
 * The tracker needs a zone on a path that has no policy in hand — a screen-off measurement happens
 * between syncs — and decrypting the policy cache inside a broadcast receiver would be file I/O on
 * the main thread. It starts as this device's own zone, which is only ever used before the first
 * policy arrives, and is replaced by the policy's on every evaluation.
 *
 * A device sitting in a different timezone from the family's is the case this exists for: the server
 * keys usage by the *policy's* day, so posting the device's day would file the minutes under a key
 * the quota never reads, and the child would have an unlimited allowance with nothing looking wrong.
 */
private class PolicyZone {
    @Volatile
    var current: ZoneId = ZoneId.systemDefault()
}

/**
 * What one sync did, in the two facts its callers need.
 *
 * [problem] is separate from the log line because it is read by a *parent*: it is what a
 * `SYNC_POLICY` command reports back to the console. A sync served from the cache has applied
 * something and still failed at the thing the parent asked for, so the two cannot be one boolean.
 */
private data class SyncTick(
    /** How many commands the server said were waiting; 0 when the sync never reached the server. */
    val pending: Int,
    /** Null when the policy came from the server and applied cleanly, else what went wrong. */
    val problem: String?,
)

/** What one measure-and-deliver did, in the three parts that can each fail separately. */
private data class ReportOutcome(
    val tick: UsageTick,
    val flush: FlushResult,
    val inventory: InventoryResult,
)

/**
 * The device's reporting half: what it measured, and what it has installed.
 *
 * Measurement and delivery are one call because the order matters — measure first, then deliver, so
 * a report always carries the window that has just ended rather than the one before it. Everything
 * it composes ([UsageTracker], [UsageReporter], [InventoryReporter]) is a plain unit with JVM tests;
 * what is left here is the wiring and the screen-state signal the poll loop waits on.
 */
private class Reporting(
    val tracker: UsageTracker,
    private val ledger: UsageLedger,
    private val zone: PolicyZone,
    private val usage: UsageReporter,
    private val inventory: InventoryReporter,
    private val wallClock: () -> Long = { System.currentTimeMillis() },
) {

    /**
     * Screen state as a flow, so the poll loop can *wait* instead of waking to find zero.
     *
     * Seeded from the clock rather than from `true`: the service starts at boot, and a loop that
     * believed the screen was on would poll every five minutes through the night.
     */
    private val awake = MutableStateFlow(tracker.screenIsOn())

    fun onScreenOn(atMonotonicMillis: Long) {
        tracker.onScreenOn(atMonotonicMillis)
        awake.value = true
    }

    fun onScreenOff(atMonotonicMillis: Long) {
        tracker.onScreenOff(atMonotonicMillis)
        awake.value = false
    }

    fun screenIsOn(): Boolean = awake.value

    suspend fun awaitScreenOn() {
        awake.first { it }
    }

    fun measureAndDeliver(): ReportOutcome {
        val tick = tracker.tick()
        if (tick is UsageTick.Measured) usage.note(tick.byDay.keys)
        return ReportOutcome(tick, usage.flush(), inventory.report())
    }

    /**
     * This device's own measurement for the policy's current day, in whole minutes.
     *
     * Also where the policy's timezone is captured, because this runs on every evaluation and is the
     * one place that is handed the [Input]. Zero when the zone cannot be read — the engine refuses
     * that input a moment later and says so loudly, and guessing a zone here would file the minutes
     * under a day nobody reads.
     */
    fun usedMinutesToday(input: Input): Int {
        val policyZone = DayAttribution.zoneOf(input.settings.timezone) ?: return 0
        zone.current = policyZone
        val day = DayAttribution.key(wallClock(), policyZone)
        // Floor, matching the server's own millis-to-minutes conversion. The two numbers are
        // combined with max, so a rounding difference of under a minute cannot change enforcement.
        return (ledger.totals(day).values.sum() / 60_000L).toInt()
    }
}

