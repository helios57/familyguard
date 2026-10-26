package io.github.helios57.familyguard.calls

import android.app.Activity
import android.app.role.RoleManager
import android.content.Intent
import android.os.Bundle
import android.util.Log
import io.github.helios57.familyguard.sync.ConnectionService

/**
 * Opens Android's own "set as call-screening app" dialog and closes again (FR-31.4). It has no screen
 * of its own. Whatever the person chose, the phone syncs at once, so the console turns from amber to
 * on — or stays amber — without waiting for the next interval.
 */
class CallGuardSetupActivity : Activity() {

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        if (savedInstanceState != null) return
        val roles = getSystemService(RoleManager::class.java)
        if (roles == null || !roles.isRoleAvailable(RoleManager.ROLE_CALL_SCREENING) ||
            roles.isRoleHeld(RoleManager.ROLE_CALL_SCREENING)
        ) {
            done()
            return
        }
        runCatching {
            @Suppress("DEPRECATION") // the result is only a trigger to sync; no contract API needed
            startActivityForResult(roles.createRequestRoleIntent(RoleManager.ROLE_CALL_SCREENING), REQUEST)
        }.onFailure {
            Log.w("CallGuard", "the role dialog would not open", it)
            done()
        }
    }

    @Deprecated("Activity result API is not used in this app")
    override fun onActivityResult(requestCode: Int, resultCode: Int, data: Intent?) {
        @Suppress("DEPRECATION")
        super.onActivityResult(requestCode, resultCode, data)
        if (requestCode == REQUEST) done()
    }

    private fun done() {
        runCatching { ConnectionService.wake(this, ConnectionService.ACTION_CALL_GUARD) }
        finish()
    }

    private companion object {
        const val REQUEST = 31
    }
}
