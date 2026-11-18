package com.chethober.xtune

import android.appwidget.AppWidgetManager
import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.content.SharedPreferences
import android.content.pm.PackageManager
import android.net.ConnectivityManager
import android.net.Network
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

    /** The connection the app last started: the core's arguments, the per-app mode and apps. */
    class Last(val args: String, val mode: String, val apps: ArrayList<String>)

    private fun lastFile(c: Context) = File(c.filesDir, "last-connection.json")

    /**
     * Kept in a file, not in the preferences: a subscription can be hundreds of KB, and Android
     * reads the whole preferences file into memory and rewrites it on every change.
     */
    @Synchronized
    fun saveLast(c: Context, args: String, mode: String, apps: List<String>) {
        val j = JSONObject().put("args", args).put("mode", mode).put("apps", JSONArray(apps))
        val f = lastFile(c)
        val tmp = File(f.path + ".tmp")
        tmp.writeText(j.toString())
        if (!tmp.renameTo(f)) {
            f.writeText(j.toString())
            tmp.delete()
        }
        prefs(c).edit().putBoolean("hasLast", true).remove("lastArgs").remove("lastMode").remove("lastApps").apply()
    }

    @Synchronized
    fun loadLast(c: Context): Last? {
        val f = lastFile(c)
        if (f.exists()) {
            return try {
                val j = JSONObject(f.readText())
                val a = j.optJSONArray("apps") ?: JSONArray()
                Last(j.getString("args"), j.optString("mode", "off"), ArrayList((0 until a.length()).map { a.getString(it) }))
            } catch (e: Exception) {
                CrashLog.write(c, "error", "read the last connection", e)
                null
            }
        }
        // 1.5 and older kept it in the preferences
        val p = prefs(c)
        val args = p.getString("lastArgs", null) ?: return null
        val a = JSONArray(p.getString("lastApps", "[]") ?: "[]")
        val apps = ArrayList((0 until a.length()).map { a.getString(it) })
        val mode = p.getString("lastMode", "off") ?: "off"
        saveLast(c, args, mode, apps)
        return Last(args, mode, apps)
    }

    fun hasLast(c: Context) = prefs(c).getBoolean("hasLast", false) || prefs(c).getString("lastArgs", null) != null

    /** The notification's Next config: the next connect starts from it too. */
    fun setSelected(c: Context, id: String) {
        val last = loadLast(c) ?: return
        val j = JSONObject(last.args)
        (j.optJSONObject("options") ?: JSONObject().also { j.put("options", it) }).put("selected", id)
        saveLast(c, j.toString(), last.mode, last.apps)
    }

    /** Starts the last connection, when there is one and the VPN permission was given. */
    fun startLast(c: Context, reason: String): Boolean {
        if (!hasLast(c)) return false
        if (VpnService.prepare(c) != null) return false
        if (XtuneVpnService.start(c, reason) != null) askToConnect(c, reason) // Android refused: ask the user
        return true
    }

    fun toggle(c: Context, reason: String) {
        if (running) {
            if (!XtuneVpnService.stop(c)) running = false
        } else if (!startLast(c, reason)) {
            // no connection yet, or the permission is missing: the app asks
            try {
                c.startActivity(Intent(c, MainActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK))
            } catch (_: Exception) {
            }
        }
    }

    // ------------------------------------------------ connecting from the background

    private const val CONNECT_NOTE = 4

    /**
     * Android 12 and later do not let an app start its VPN from the background (Wi-Fi, a late
     * alarm) unless it is exempt from battery optimization. Then a notification asks instead: a
     * tap opens the app, which connects.
     */
    fun askToConnect(c: Context, reason: String) {
        try {
            val fa = prefs(c).getString("lang", "en") == "fa"
            val nm = c.getSystemService(android.app.NotificationManager::class.java) ?: return
            if (Build.VERSION.SDK_INT >= 26) {
                nm.createNotificationChannel(
                    android.app.NotificationChannel("connect", if (fa) "اتصال" else "Connecting", android.app.NotificationManager.IMPORTANCE_DEFAULT)
                )
            }
            val open = PendingIntent.getActivity(
                c, 12, Intent(c, MainActivity::class.java).setData(android.net.Uri.parse("xtune://connect?reason=$reason"))
                    .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK),
                PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT
            )
            val b = if (Build.VERSION.SDK_INT >= 26) android.app.Notification.Builder(c, "connect") else @Suppress("DEPRECATION") android.app.Notification.Builder(c)
            nm.notify(
                CONNECT_NOTE,
                b.setSmallIcon(R.drawable.ic_vpn)
                    .setContentTitle(if (fa) "برای اتصال بزنید" else "Tap to connect")
                    .setContentText(
                        if (fa) "اندروید نگذاشت xtune در پس‌زمینه وصل شود"
                        else "Android did not let xtune connect in the background"
                    )
                    .setContentIntent(open)
                    .setAutoCancel(true)
                    .build()
            )
        } catch (e: Exception) {
            CrashLog.write(c, "error", "ask to connect", e)
        }
    }

    fun cancelAskToConnect(c: Context) {
        try {
            c.getSystemService(android.app.NotificationManager::class.java)?.cancel(CONNECT_NOTE)
        } catch (_: Exception) {
        }
    }

    /** Exempt from battery optimization: Android then lets it connect from the background. */
    fun batteryExempt(c: Context): Boolean {
        if (Build.VERSION.SDK_INT < 23) return true
        return c.getSystemService(android.os.PowerManager::class.java)?.isIgnoringBatteryOptimizations(c.packageName) ?: false
    }

    /** The VPN came up or went down: the tile and the widget follow. */
    fun setRunning(c: Context, on: Boolean) {
        running = on
        if (on) cancelAskToConnect(c)
        try {
            TileService.requestListeningState(c, ComponentName(c, XtuneTileService::class.java))
        } catch (_: Exception) {
        }
        try {
            // null on devices without home-screen widgets
            val wm: AppWidgetManager? = AppWidgetManager.getInstance(c)
            val ids = wm?.getAppWidgetIds(ComponentName(c, XtuneWidget::class.java)) ?: IntArray(0)
            if (wm != null && ids.isNotEmpty()) XtuneWidget.update(c, wm, ids)
        } catch (_: Exception) {
        }
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
            try {
                cm.registerNetworkCallback(req, pi)
            } catch (e: Exception) {
                CrashLog.write(c, "error", "watch Wi-Fi", e)
            }
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
        if (captive(c)) {
            // a hotel or airport Wi-Fi: the VPN would block its sign-in page; connect once it is done
            waitForSignIn(c)
            return
        }
        startLast(c, "wifi")
    }

    // ------------------------------------------------ captive portals

    private const val CAPTIVE_NOTE = 3
    @Volatile
    private var waiting: ConnectivityManager.NetworkCallback? = null

    /** The current network wants a sign-in first (or has not been checked as online yet). */
    fun captive(c: Context): Boolean {
        val cm = c.getSystemService(ConnectivityManager::class.java) ?: return false
        val caps = cm.getNetworkCapabilities(cm.activeNetwork ?: return false) ?: return false
        return caps.hasCapability(NetworkCapabilities.NET_CAPABILITY_CAPTIVE_PORTAL)
    }

    /** Asks for the sign-in, then connects once Android sees the network online. */
    fun waitForSignIn(c: Context) {
        val cm = c.getSystemService(ConnectivityManager::class.java) ?: return
        val net = cm.activeNetwork ?: return
        val fa = prefs(c).getString("lang", "en") == "fa"
        val nm = c.getSystemService(android.app.NotificationManager::class.java)
        if (Build.VERSION.SDK_INT >= 26) {
            nm.createNotificationChannel(android.app.NotificationChannel("wifi", if (fa) "وای‌فای" else "Wi-Fi", android.app.NotificationManager.IMPORTANCE_DEFAULT))
        }
        val signIn = PendingIntent.getActivity(
            c, 9, Intent(Intent.ACTION_VIEW, android.net.Uri.parse("http://neverssl.com")).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK),
            PendingIntent.FLAG_IMMUTABLE
        )
        val b = if (Build.VERSION.SDK_INT >= 26) android.app.Notification.Builder(c, "wifi") else @Suppress("DEPRECATION") android.app.Notification.Builder(c)
        nm.notify(
            CAPTIVE_NOTE,
            b.setSmallIcon(R.drawable.ic_vpn)
                .setContentTitle(if (fa) "این وای‌فای ورود می‌خواهد" else "This Wi-Fi wants you to sign in")
                .setContentText(if (fa) "وارد شوید؛ xtune بعد از آن وصل می‌شود" else "Sign in, and xtune connects once you're online")
                .setContentIntent(signIn)
                .setAutoCancel(true)
                .build()
        )
        waiting?.let { try { cm.unregisterNetworkCallback(it) } catch (_: Exception) {} }
        val cb = object : ConnectivityManager.NetworkCallback() {
            override fun onCapabilitiesChanged(n: Network, caps: NetworkCapabilities) {
                if (n == net && caps.hasCapability(NetworkCapabilities.NET_CAPABILITY_VALIDATED) &&
                    !caps.hasCapability(NetworkCapabilities.NET_CAPABILITY_CAPTIVE_PORTAL)
                ) {
                    done(this)
                    if (!running) startLast(c, "wifi")
                }
            }

            override fun onLost(n: Network) {
                if (n == net) done(this)
            }

            fun done(cb: ConnectivityManager.NetworkCallback) {
                nm.cancel(CAPTIVE_NOTE)
                try {
                    cm.unregisterNetworkCallback(cb)
                } catch (_: Exception) {
                }
                waiting = null
            }
        }
        waiting = cb
        try {
            cm.registerNetworkCallback(NetworkRequest.Builder().addTransportType(NetworkCapabilities.TRANSPORT_WIFI).build(), cb)
        } catch (e: Exception) {
            waiting = null
            CrashLog.write(c, "error", "wait for the Wi-Fi sign-in", e)
        }
    }
}
