plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
}

android {
    namespace = "com.wthomson.ctrlfreak"
    compileSdk = 34

    defaultConfig {
        applicationId = "com.wthomson.ctrlfreak"
        minSdk = 26
        targetSdk = 34
        versionCode = 1
        versionName = "1.0"
    }

    buildTypes {
        // Debug builds are what CI produces for sideloading. Release is here for
        // completeness; sign it if you ever publish.
        getByName("release") {
            isMinifyEnabled = false
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlinOptions {
        jvmTarget = "17"
    }

    // Kotlin sources live under src/main/kotlin.
    sourceSets["main"].java.srcDirs("src/main/kotlin")
}

dependencies {
    implementation("androidx.core:core-ktx:1.13.1")
    implementation("androidx.appcompat:appcompat:1.7.0")
    implementation("androidx.activity:activity-ktx:1.9.2")
    implementation("androidx.lifecycle:lifecycle-runtime-ktx:2.8.4")
    implementation("com.google.android.material:material:1.12.0")
    implementation("org.jetbrains.kotlinx:kotlinx-coroutines-android:1.8.1")
    implementation("com.squareup.okhttp3:okhttp:4.12.0")
    implementation("com.google.code.gson:gson:2.11.0")
    // Community WebRTC build for Android (Google no longer publishes official
    // artifacts). If this exact version ever fails to resolve, bump it to the
    // latest io.github.webrtc-sdk:android release.
    implementation("io.github.webrtc-sdk:android:125.6422.07")
}
