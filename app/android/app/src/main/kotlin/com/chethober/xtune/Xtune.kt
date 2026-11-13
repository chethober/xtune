package com.chethober.xtune

import android.appwidget.AppWidgetManager
import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.content.SharedPreferences
import android.content.pm.PackageManager
import android.net.ConnectivityManager
import android.net.NetworkCapabilities
import android.net.NetworkRequest
import android.net.VpnService
import android.app.PendingIntent
import android.net.wifi.WifiManager
import android.os.Build
import android.service.quicksettings.TileService
import mobile.Mobile
import org.json.JSONArray
import org.json.JSONObject
import java.io.File

/**
 * What the app's background entry points share: the system's always-on VPN, connecting at boot
 * and on untrusted Wi-Fi, the Quick Settings tile and the widget. They reconnect with the last
 * connection the app made, which the VPN service keeps here.
 */
object Xtune {
    private const val PREFS = "xtune"
    @Volatile
    var running = false
        private set
    @Volatile
    private var setUp = false

    fun prefs(c: Context): SharedPreferences = c.getSharedPreferences(PREFS, Context.MODE_PRIVATE)

    /** Copies the geo databases out of the APK once per app version and tells the core where they and its log live. */
    @Synchronized
    fun setup(c: Context) {
        if (setUp) return
        val dir = File(c.filesDir, "geo")
        val stamp = File(dir, ".version")
        val version = try {
            c.packageManager.getPackageInfo(c.packageName, 0).let {
                if (Build.VERSION.SDK_INT >= 28) it.longVersionCode.toString() else @Suppress("DEPRECATION") it.versionCode.toString()
            }
        } catch (e: PackageManager.NameNotFoundException) {
            "0"
        }
        if (!stamp.exists() || stamp.readText() != version) {
            dir.mkdirs()
            for (name in listOf("geoip.dat", "geosite.dat")) {
                try {
                    c.assets.open("geo/$name").use { input -> File(dir, name).outputStream().use { input.copyTo(it) } }
                } catch (_: Exception) {
                }
            }
            stamp.writeText(version)
        }
        val data = File(c.filesDir, "core").apply { mkdirs() }
        Mobile.call("setup", JSONObject().put("assetDir", dir.absolutePath).put("dataDir", data.absolutePath).toString())
        setUp = true
    }

    /** The connection the app last started: its arguments, per-app mode and apps. */
    fun saveLast(c: Context, args: String, mode: String, apps: List<String>) {
        prefs(c).edit().putString("lastArgs", args).putString("lastMode", mode).putString("lastApps", JSONArray(apps).toString()).apply()
    }

    fun hasLast(c: Context) = prefs(c).getString("lastArgs", null) != null

    /** Starts the last connection, when there is one and the VPN permission was given. */
    fun startLast(c: Context): Boolean {
        val p = prefs(c)
        val args = p.getString("lastArgs", null) ?: return false
        if (VpnService.prepare(c) != null) return false
        val apps = JSONArray(p.getString("lastApps", "[]") ?: "[]").let { a -> ArrayList((0 until a.length()).map { a.getString(it) }) }
        XtuneVpnService.start(c, args, p.getString("lastMode", "off") ?: "off", apps)
        return true
    }

    fun toggle(c: Context) {
        if (running) {
            XtuneVpnService.stop(c)
        } else if (!startLast(c)) {
            // no connection yet, or the permission is missing: the app asks
            c.startActivity(Intent(c, MainActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK))
        }
    }

    /** The VPN came up or went down: the tile and the widget follow. */
    fun setRunning(c: Context, on: Boolean) {
        running = on
        try {
            TileService.requestListeningState(c, ComponentName(c, XtuneTileService::class.java))
        } catch (_: Exception) {
        }
        val wm = AppWidgetManager.getInstance(c)
        val ids = wm.getAppWidgetIds(ComponentName(c, XtuneWidget::class.java))
        if (ids.isNotEmpty()) XtuneWidget.update(c, wm, ids)
    }

    // ------------------------------------------------ auto-connect

    fun setAuto(c: Context, boot: Boolean, wifi: Boolean, trusted: List<String>) {
        prefs(c).edit().putBoolean("autoBoot", boot).putBoolean("autoWifi", wifi).putString("trusted", JSONArray(trusted).toString()).apply()
        watchWifi(c, wifi)
    }

    /** Wi-Fi changes arrive as broadcasts even while the app is closed (a PendingIntent callback). */
    fun watchWifi(c: Context, on: Boolean) {
        val cm = c.getSystemService(ConnectivityManager::class.java) ?: return
        val pi = PendingIntent.getBroadcast(
            c, 7, Intent(c, WifiReceiver::class.java),
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_MUTABLE
        )
        try {
            cm.unregisterNetworkCallback(pi)
        } catch (_: Exception) {
        }
        if (on) {
            val req = NetworkRequest.Builder().addTransportType(NetworkCapabilities.TRANSPORT_WIFI).build()
            cm.registerNetworkCallback(req, pi)
        }
    }

    /** The current Wi-Fi's name, or "" when Android does not tell (no location permission). */
    fun currentSsid(c: Context): String {
        val wm = c.applicationContext.getSystemService(WifiManager::class.java) ?: return ""
        @Suppress("DEPRECATION")
        val s = wm.connectionInfo?.ssid ?: return ""
        return if (s == "<unknown ssid>") "" else s.trim('"')
    }

    fun onWifi(c: Context) {
        val p = prefs(c)
        if (!p.getBoolean("autoWifi", false) || running) return
        val trusted = JSONArray(p.getString("trusted", "[]") ?: "[]").let { a -> (0 until a.length()).map { a.getString(it) } }
        val ssid = currentSsid(c)
        if (ssid.isNotEmpty() && trusted.contains(ssid)) return
        startLast(c)
    }
}
