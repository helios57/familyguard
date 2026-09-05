// Repositories are declared here and nowhere else. FAIL_ON_PROJECT_REPOS turns a repository added
// in a module's own build file into a build error rather than a silent second source of artifacts —
// which is how a dependency ends up resolved from somewhere nobody audited.
pluginManagement {
    repositories {
        google()
        mavenCentral()
        gradlePluginPortal()
    }
}

dependencyResolutionManagement {
    repositoriesMode.set(RepositoriesMode.FAIL_ON_PROJECT_REPOS)
    repositories {
        google()
        mavenCentral()
    }
}

rootProject.name = "familyguard"
include(":app")

// A second, tiny application whose only purpose is to be installed by the managed-install tests.
// See fixture-app/build.gradle.kts.
include(":fixture-app")
