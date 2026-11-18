package com.chethober.xtune

import android.app.Application
import android.content.Context
import android.os.Build
import java.io.File
import java.io.PrintWriter
import java.io.StringWriter

/**
 * Errors kept on disk so the app can show them later (Settings → Diagnostics → Crash log): the
 * uncaught ones that close the app, and the ones the app caught and carried on after. The Go core
 * writes its own crashes to the same folder (core/crash), and reads them all for the app.
 */
object CrashLog {
    private const val KEEP = 30

    fun dir(c: Context): File = File(c.filesDir, "core/crash").apply { mkdirs() }

    fun install(app: Application) {
        val previous = Thread.getDefaultUncaughtExceptionHandler()
        Thread.setDefaultUncaughtExceptionHandler { t, e ->
            try {
                write(app, "crash", "thread ${t.name}", e)
            } catch (_: Throwable) {
            }
            previous?.uncaughtException(t, e)
        }
    }

    /** kind: crash (the app closed) | error (caught: the app carried on). */
    fun write(c: Context, kind: String, where: String, e: Throwable) {
        val sw = StringWriter()
        e.printStackTrace(PrintWriter(sw))
        val version = try {
            c.packageManager.getPackageInfo(c.packageName, 0).versionName ?: "?"
        } catch (_: Exception) {
            "?"
        }
        val text = buildString {
            append("kind: ").append(kind).append('\n')
            append("source: android\n")
            append("where: ").append(where).append('\n')
            append("app: ").append(version).append('\n')
            append("android: ").append(Build.VERSION.RELEASE).append(" (API ").append(Build.VERSION.SDK_INT).append(")\n")
            append("device: ").append(Build.MANUFACTURER).append(' ').append(Build.MODEL).append("\n\n")
            append(sw.toString())
        }
        val d = dir(c)
        File(d, "${System.currentTimeMillis()}-android-$kind.txt").writeText(text)
        d.listFiles()?.sortedByDescending { it.name }?.drop(KEEP)?.forEach { it.delete() }
    }

    /** Runs f, and keeps what it throws instead of closing the app. */
    inline fun guard(c: Context, where: String, f: () -> Unit) {
        try {
            f()
        } catch (e: Throwable) {
            try {
                write(c, "error", where, e)
            } catch (_: Throwable) {
            }
        }
    }
}

/** The app's process: errors that would close it are written down first. */
class XtuneApp : Application() {
    override fun onCreate() {
        super.onCreate()
        CrashLog.install(this)
    }
}
