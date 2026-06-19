# Keep gomobile generated classes (JNI bridge)
-keep class com.flowdav.app.flowdavmobile.** { *; }
-keep class go.** { *; }

# Keep Kotlin coroutines internals
-keepnames class kotlinx.coroutines.internal.MainDispatcherFactory {}
-keepnames class kotlinx.coroutines.CoroutineExceptionHandler {}

# Tink (used by security-crypto) references javax.annotation classes
# which are compile-only and not needed at runtime
-dontwarn javax.annotation.Nullable
-dontwarn javax.annotation.concurrent.GuardedBy
