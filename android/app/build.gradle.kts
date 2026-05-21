plugins {
    id("com.android.application")
}

val appVersion = (
    System.getenv("ANDROID_APP_VERSION")?.removePrefix("v")
    ?: providers.exec { commandLine("git", "describe", "--tags", "--always", "--dirty") }.standardOutput.asText.get().trim().removePrefix("v")
).also { v -> if (v.isBlank()) throw GradleException("Cannot determine version") }

val appCode = maxOf(
    try {
        val parts = appVersion.split(".").take(3).map { it.filter(Char::isDigit).toIntOrNull() ?: 0 }
        parts.getOrElse(0) { 0 } * 100000 + parts.getOrElse(1) { 0 } * 1000 + parts.getOrElse(2) { 0 }
    } catch (e: Exception) { 0 },
    1
)

android {
    namespace = "com.flowdav.app"
    compileSdk = 36

    defaultConfig {
        applicationId = "com.flowdav.app"
        minSdk = 26
        targetSdk = 36
        versionCode = appCode
        versionName = appVersion
    }

    signingConfigs {
        val keystorePath = System.getenv("ANDROID_KEYSTORE_PATH")
        if (!keystorePath.isNullOrEmpty()) {
            create("release") {
                storeFile = file(keystorePath)
                storePassword = System.getenv("ANDROID_KEYSTORE_PASSWORD") ?: ""
                keyAlias = System.getenv("ANDROID_KEY_ALIAS") ?: ""
                keyPassword = System.getenv("ANDROID_KEYSTORE_PASSWORD") ?: ""
            }
        }
    }

    buildTypes {
        release {
            signingConfig = signingConfigs.findByName("release")
            isMinifyEnabled = true
            proguardFiles(
                getDefaultProguardFile("proguard-android-optimize.txt"),
                "proguard-rules.pro"
            )
        }
    }

    buildFeatures {
        viewBinding = true
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
}

dependencies {
    implementation(files("libs/flowdav.aar"))
    implementation("androidx.appcompat:appcompat:1.7.1")
    implementation("androidx.core:core-ktx:1.18.0")
    implementation("com.google.android.material:material:1.14.0")
    implementation("androidx.activity:activity-ktx:1.13.0")
    implementation("androidx.lifecycle:lifecycle-viewmodel-ktx:2.10.0")
    implementation("androidx.lifecycle:lifecycle-runtime-ktx:2.10.0")
    implementation("androidx.security:security-crypto:1.1.0")
}
