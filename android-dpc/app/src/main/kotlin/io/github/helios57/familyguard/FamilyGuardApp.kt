package io.github.helios57.familyguard

import android.app.Application
import io.github.helios57.familyguard.push.PushRegistrar

class FamilyGuardApp : Application() {
    override fun onCreate() {
        super.onCreate()
        // FR-26.3: a push can be what starts this process, and Firebase must be up to hand it over.
        PushRegistrar.resume(this)
    }
}
