plugins {
    id("com.android.application")
}

android {
    namespace = "ca.proveo.hello"
    compileSdk = 36

    defaultConfig {
        applicationId = "ca.proveo.hello"
        minSdk = 24
        targetSdk = 36
        versionCode = 1
        versionName = "1.0"
    }
}
