package io.github.helios57.familyguard.filter

import android.content.Context
import io.github.helios57.familyguard.policy.AlwaysOnVpnManager
import io.github.helios57.familyguard.policy.AlwaysOnVpnOutcome
import io.github.helios57.familyguard.sync.FilterApplier
import io.github.helios57.familyguard.sync.FilterGateway
import io.github.helios57.familyguard.sync.StateApplier

/**
 * [FilterGateway] over the real device. Thin on purpose: every decision is in [FilterApplier].
 *
 * The one thing it adds is where the calls land — [FilterState] for what is persisted and compiled,
 * [AlwaysOnVpnManager] for the platform's consent, and [AdFilterVpnService] for the tunnel itself.
 * Three owners, because each of them is separately able to fail and separately able to be read back.
 */
class AndroidFilterGateway(
    context: Context,
    private val alwaysOn: AlwaysOnVpnManager,
) : FilterGateway {

    private val context = context.applicationContext

    override fun remember(enabled: Boolean, listUrl: String) =
        FilterState.remember(context, FilterPolicy(enabled = enabled), listUrl)

    override fun listState(): FilterListState = FilterState.listState(context)

    override fun refresh(): RefreshResult = FilterState.refresh(context)

    override fun setAlwaysOn(enabled: Boolean): AlwaysOnVpnOutcome = alwaysOn.apply(enabled)

    override fun setRunning(running: Boolean) {
        if (running) AdFilterVpnService.apply(context) else AdFilterVpnService.stop(context)
    }

    override fun explain(reason: String) = AdFilterVpnService.explain(reason)
}

/**
 * The FR-6.6 applier as the sync path builds it, or null on a build that must not filter.
 *
 * Null rather than an applier that does nothing, because the two are different claims: a null
 * applier leaves the filter untouched, and one that "does nothing" would still have to decide
 * whether that means stopping a tunnel somebody started. On a Play build there is no tunnel to
 * stop — `AD_FILTER_AVAILABLE` is false and the service refuses to start — so there is nothing for
 * an applier to be right about.
 */
fun filterApplier(context: Context, alwaysOn: AlwaysOnVpnManager): StateApplier? {
    if (!io.github.helios57.familyguard.BuildConfig.AD_FILTER_AVAILABLE) return null
    return FilterApplier(AndroidFilterGateway(context, alwaysOn))
}
