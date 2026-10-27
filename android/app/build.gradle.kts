plugins {
    id("com.android.application")
}

val appVersion = (
    System.getenv("ANDROID_APP_VERSION")?.removePrefix("v")
        ?: providers.exec {
            commandLine("git", "describe", "--tags", "--always", "--dirty")
        }.standardOutput.asText.get().trim().removePrefix("v")
    ).also { v -> if (v.isBlank()) throw GradleException("Cannot determine version") }

val appCode = maxOf(
    try {
        val parts = appVersion.split(".").take(3).map { it.filter(Char::isDigit).toIntOrNull() ?: 0 }
        parts.getOrElse(0) { 0 } * 100000 + parts.getOrElse(1) { 0 } * 1000 + parts.getOrElse(2) { 0 }
    } catch (e: Exception) {
        0
    },
    1
)

// ErrorKindCodesTest reads the Go bridge source; declare it as an input or Gradle
// skips the test when only that file changes.
tasks.withType<org.gradle.api.tasks.testing.Test>().configureEach {
    inputs.file(layout.projectDirectory.file("../../cmd/android/bridge.go"))
        .withPropertyName("bridgeGoSource")
}

kotlin {
    compilerOptions {
        allWarningsAsErrors.set(true)
    }
}

android {
    namespace = "com.flowdav.app"
    compileSdk = 37

    defaultConfig {
        applicationId = "com.flowdav.app"
        minSdk = 26
        targetSdk = 37
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
            isShrinkResources = true
            proguardFiles(
                getDefaultProguardFile("proguard-android-optimize.txt"),
                "proguard-rules.pro"
            )
        }
    }

    lint {
        warningsAsErrors = true
        abortOnError = true
        // Version-availability checks duplicate Dependabot and fail CI the
        // moment a newer Gradle/AGP is released; they are not code-quality
        // issues (AGENTS.md: tool versions are Dependabot's concern).
        disable += "AndroidGradlePluginVersion"
    }

    buildFeatures {
        viewBinding = true
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
}

// Reproducible bytecode target: explicit toolchain (developer.android.com/build/jdks);
// foojay in settings.gradle.kts provisions JDK 17 when absent.
java {
    toolchain {
        languageVersion = JavaLanguageVersion.of(17)
    }
}

dependencies {
    implementation(files("libs/flowdav.aar"))
    implementation("androidx.appcompat:appcompat:1.8.0")
    implementation("androidx.core:core-ktx:1.19.1")
    implementation("com.google.android.material:material:1.14.0")
    implementation("androidx.activity:activity-ktx:1.13.0")
    implementation("androidx.lifecycle:lifecycle-viewmodel-ktx:2.11.0")
    implementation("androidx.lifecycle:lifecycle-runtime-ktx:2.11.0")
    implementation("androidx.security:security-crypto:1.1.0")
    // Offline QR scanning without Play Services (privacy-first: no Google deps).
    // Latest release is 4.3.0 (Oct 2021) but the library is stable and
    // self-contained; the alternative (ML Kit) requires Play Services.
    implementation("com.journeyapps:zxing-android-embedded:4.3.0")
    testImplementation("junit:junit:4.13.2")
}
