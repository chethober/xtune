package com.chethober.xtune

import android.app.AlarmManager
import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.graphics.drawable.Icon
import android.net.VpnService
import android.os.Build
import android.os.Handler
import android.os.Looper
import android.os.ParcelFileDescriptor
import android.os.PowerManager
import android.os.SystemClock
import mobile.Mobile
import org.json.JSONObject
import java.text.DateFormat
import java.util.Date
import java.util.concurrent.Executors

/**
 * The whole-device connection: Android hands over a TUN descriptor, and the Go core's Xray reads
 * every packet from it. This app is left out of the VPN, so the core's own connections to the
 * servers go out directly instead of looping back in.
 *
 * Its notification is the remote control: the config in use and live speeds, with Disconnect,
 * Next config and Pause 15 min. A pause closes the tunnel (traffic goes direct) but keeps the
 * service, and its notification, until it reconnects by itself or Resume now is pressed.
 */
class XtuneVpnService : VpnService() {
    companion object {
        const val ACTION_START = "com.chethober.xtune.START"
        const val ACTION_STOP = "com.chethober.xtune.STOP"
        const val ACTION_NEXT = "com.chethober.xtune.NEXT"
        const val ACTION_PAUSE = "com.chethober.xtune.PAUSE"
        const val ACTION_RESUME = "com.chethober.xtune.RESUME"
        private const val CHANNEL = "vpn"
        private const val NOTIFICATION = 1
        private const val PAUSE_MS = 15 * 60 * 1000L
        private const val TICK_MS = 2000L

        @Volatile
        var onStarted: ((String) -> Unit)? = null

        /** Paused, and when it reconnects (wall clock ms); 0 when not paused. */
        @Volatile
        var pausedUntil = 0L
            private set

        /** mode: off, bypass (apps skip the VPN) or only (only apps use it). */
        /** reason is what started it, for the timeline: boot, wifi, tile, widget; "" from the app. */
        fun start(context: Context, args: String, mode: String, apps: ArrayList<String>, reason: String = "") {
            val i = Intent(context, XtuneVpnService::class.java).setAction(ACTION_START)
                .putExtra("args", args).putExtra("mode", mode).putStringArrayListExtra("apps", apps).putExtra("reason", reason)
            if (Build.VERSION.SDK_INT >= 26) context.startForegroundService(i) else context.startService(i)
        }

        fun stop(context: Context) {
            context.startService(Intent(context, XtuneVpnService::class.java).setAction(ACTION_STOP))
        }
    }

    private var tun: ParcelFileDescriptor? = null
    private val worker = Executors.newSingleThreadExecutor() // the core's calls, in order
    private val main = Handler(Looper.getMainLooper())
    private var status = JSONObject()
    private var lastUp = -1L
    private var lastDown = -1L
    private var lastAt = 0L
    private var upRate = 0.0
    private var downRate = 0.0
    private var note: String? = null // a passing message ("No other config"), shown once

    private val tick = object : Runnable {
        override fun run() {
            val screenOn = getSystemService(PowerManager::class.java)?.isInteractive ?: true
            if (tun != null && pausedUntil == 0L && screenOn) worker.execute { refresh() }
            main.postDelayed(this, TICK_MS)
        }
    }

    private val resumeLater = Runnable { startService(Intent(this, XtuneVpnService::class.java).setAction(ACTION_RESUME)) }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        when (intent?.action) {
            ACTION_STOP -> {
                worker.execute { shutdown() }
                return START_NOT_STICKY
            }
            ACTION_NEXT -> {
                worker.execute { next() }
                return START_NOT_STICKY
            }
            ACTION_PAUSE -> {
                worker.execute { pause() }
                return START_NOT_STICKY
            }
            ACTION_RESUME -> {
                foreground(connecting = true)
                worker.execute { begin(null, null, null, "resume") }
                return START_NOT_STICKY
            }
        }
        foreground(connecting = true)
        val args = intent?.getStringExtra("args")
        val mode = intent?.getStringExtra("mode")
        val apps = intent?.getStringArrayListExtra("apps")
        // no arguments: the system started it (always-on VPN)
        val reason = intent?.getStringExtra("reason") ?: if (args == null) "always-on" else ""
        worker.execute { begin(args, mode, apps, reason) }
        return START_NOT_STICKY
    }

    /** Connects with these arguments, or the last connection's (always-on VPN, Resume, the tile). */
    private fun begin(argsIn: String?, modeIn: String?, appsIn: ArrayList<String>?, reason: String) {
        cancelResume()
        var args = argsIn
        var mode = modeIn ?: "off"
        var apps = appsIn ?: arrayListOf()
        if (args != null) {
            Xtune.saveLast(this, args, mode, apps)
        } else {
            val p = Xtune.prefs(this)
            args = p.getString("lastArgs", null)
            if (args == null) {
                shutdown()
                return
            }
            mode = p.getString("lastMode", "off") ?: "off"
            apps = org.json.JSONArray(p.getString("lastApps", "[]") ?: "[]").let { a -> ArrayList((0 until a.length()).map { a.getString(it) }) }
        }
        val reply = try {
            Xtune.setup(this)
            connect(args, mode, apps, reason)
        } catch (e: Exception) {
            JSONObject().put("ok", false).put("error", e.message ?: e.toString()).toString()
        }
        if (JSONObject(reply).optBoolean("ok")) {
            Xtune.setRunning(this, true)
            lastUp = -1
            refresh()
            main.removeCallbacks(tick)
            main.postDelayed(tick, TICK_MS)
        } else {
            shutdown()
        }
        onStarted?.invoke(reply)
        onStarted = null
    }

    private fun connect(args: String, mode: String, apps: List<String>, reason: String): String {
        Mobile.call("vpn.stop", "{\"reason\":\"restart\"}")
        tun?.close()
        val j = JSONObject(args)
        if (reason.isNotEmpty()) j.put("reason", reason)
        val opts = j.optJSONObject("options") ?: JSONObject()
        val b = Builder()
            .setSession("xtune")
            .setMtu(1500)
            .addAddress("172.19.0.1", 30)
            .addRoute("0.0.0.0", 0)
            .addDnsServer("1.1.1.1")
        // IPv6 always comes into the tunnel, also when it is off: Xray then blocks it. Left out of
        // the VPN, apps' IPv6 traffic would go around it (a leak).
        b.addAddress("fd66:7874:756e::1", 126).addRoute("::", 0)
        // Android takes either an allow list or a deny list, not both. This app is never in the
        // VPN: in "only" mode it is simply not on the list.
        val chosen = apps.filter { it != packageName }
        if (mode == "only" && chosen.isNotEmpty()) {
            for (p in chosen) {
                try {
                    b.addAllowedApplication(p)
                } catch (_: Exception) {
                }
            }
        } else {
            b.addDisallowedApplication(packageName)
            if (mode == "bypass") {
                for (p in chosen) {
                    try {
                        b.addDisallowedApplication(p)
                    } catch (_: Exception) {
                    }
                }
            }
        }
        if (Build.VERSION.SDK_INT >= 29) b.setMetered(false)
        val pfd = b.establish() ?: throw IllegalStateException("Android did not create the VPN (permission revoked?)")
        tun = pfd
        opts.put("mode", "tun").put("tunFd", pfd.fd).put("routes", false)
        j.put("options", opts)
        return Mobile.call("vpn.start", j.toString())
    }

    /** The next config in Auto's order; kept for the tile, the widget and always-on too. */
    private fun next() {
        if (tun == null || pausedUntil != 0L) return
        val r = JSONObject(Mobile.call("vpn.next", "{}"))
        if (!r.optBoolean("ok")) {
            note = r.optString("error").ifEmpty { null }
        } else {
            val sel = r.optJSONObject("result")?.optString("selected") ?: ""
            val p = Xtune.prefs(this)
            p.getString("lastArgs", null)?.let { last ->
                val j = JSONObject(last)
                (j.optJSONObject("options") ?: JSONObject().also { j.put("options", it) }).put("selected", sel)
                p.edit().putString("lastArgs", j.toString()).apply()
            }
        }
        lastUp = -1
        refresh()
    }

    /** Closes the tunnel for 15 minutes: traffic goes direct, then it reconnects by itself. */
    private fun pause() {
        if (tun == null) return
        Mobile.call("vpn.stop", "{\"reason\":\"pause\"}")
        tun?.close()
        tun = null
        pausedUntil = System.currentTimeMillis() + PAUSE_MS
        Xtune.setRunning(this, false)
        main.removeCallbacks(tick)
        // a Handler alone stops while the phone sleeps; the alarm wakes it (within minutes in Doze)
        main.postDelayed(resumeLater, PAUSE_MS)
        getSystemService(AlarmManager::class.java)?.setAndAllowWhileIdle(
            AlarmManager.ELAPSED_REALTIME_WAKEUP, SystemClock.elapsedRealtime() + PAUSE_MS, resumeIntent()
        )
        show()
    }

    private fun resumeIntent(): PendingIntent {
        val i = Intent(this, XtuneVpnService::class.java).setAction(ACTION_RESUME)
        val f = PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT
        // the alarm may come after Android stopped the app: then it must start a foreground service
        return if (Build.VERSION.SDK_INT >= 26) PendingIntent.getForegroundService(this, 4, i, f) else PendingIntent.getService(this, 4, i, f)
    }

    private fun cancelResume() {
        pausedUntil = 0L
        main.removeCallbacks(resumeLater)
        getSystemService(AlarmManager::class.java)?.cancel(resumeIntent())
    }

    /** Reads the core's status and redraws the notification with the config and the speeds. */
    private fun refresh() {
        val r = JSONObject(Mobile.call("vpn.status", "{}"))
        status = r.optJSONObject("result") ?: JSONObject()
        val up = status.optLong("up")
        val down = status.optLong("down")
        val now = SystemClock.elapsedRealtime()
        if (lastUp >= 0 && up >= lastUp && down >= lastDown && now > lastAt) {
            val secs = (now - lastAt) / 1000.0
            upRate = (up - lastUp) / secs
            downRate = (down - lastDown) / secs
        } else {
            upRate = 0.0
            downRate = 0.0
        }
        lastUp = up
        lastDown = down
        lastAt = now
        show()
    }

    private fun channel() {
        if (Build.VERSION.SDK_INT >= 26) {
            val nm = getSystemService(NotificationManager::class.java)
            val ch = NotificationChannel(CHANNEL, "VPN", NotificationManager.IMPORTANCE_LOW)
            ch.setShowBadge(false)
            nm.createNotificationChannel(ch)
        }
    }

    private fun foreground(connecting: Boolean) {
        channel()
        val n = build(connecting)
        try {
            if (Build.VERSION.SDK_INT >= 34) {
                startForeground(NOTIFICATION, n, ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE)
            } else {
                startForeground(NOTIFICATION, n)
            }
        } catch (e: Exception) {
            // Android refused a start from the background (a late resume alarm): stay off; the
            // app, the tile or always-on connect again
            stopSelf()
        }
    }

    private fun show() {
        getSystemService(NotificationManager::class.java).notify(NOTIFICATION, build(false))
    }

    private fun action(icon: Int, title: String, act: String, code: Int): Notification.Action {
        val pi = PendingIntent.getService(this, code, Intent(this, XtuneVpnService::class.java).setAction(act), PendingIntent.FLAG_IMMUTABLE)
        return Notification.Action.Builder(Icon.createWithResource(this, icon), title, pi).build()
    }

    private fun build(connecting: Boolean): Notification {
        val s = Strings.of(this)
        val open = PendingIntent.getActivity(this, 0, Intent(this, MainActivity::class.java), PendingIntent.FLAG_IMMUTABLE)
        val b = if (Build.VERSION.SDK_INT >= 26) Notification.Builder(this, CHANNEL) else @Suppress("DEPRECATION") Notification.Builder(this)
        b.setSmallIcon(R.drawable.ic_vpn)
            .setContentIntent(open)
            .setOngoing(true)
            .setOnlyAlertOnce(true)
            .setShowWhen(false)
            .setCategory(Notification.CATEGORY_SERVICE)
            .setVisibility(Notification.VISIBILITY_PUBLIC)
        val stop = action(R.drawable.ic_vpn, s.disconnect, ACTION_STOP, 1)
        when {
            pausedUntil != 0L -> {
                val at = DateFormat.getTimeInstance(DateFormat.SHORT).format(Date(pausedUntil))
                b.setContentTitle(s.paused).setContentText(s.reconnectsAt(at))
                    .addAction(action(R.drawable.ic_vpn, s.resume, ACTION_RESUME, 3))
                    .addAction(stop)
            }
            connecting || tun == null -> b.setContentTitle(s.connecting)
            else -> {
                val name = status.optString("activeName").ifEmpty { s.connected }
                val auto = status.optString("selected").isEmpty()
                b.setContentTitle(if (auto) "${s.auto} · $name" else name)
                    .setContentText(note ?: "↓ ${rate(downRate)}   ↑ ${rate(upRate)}")
                    .addAction(stop)
                    .addAction(action(R.drawable.ic_vpn, s.next, ACTION_NEXT, 2))
                    .addAction(action(R.drawable.ic_vpn, s.pause, ACTION_PAUSE, 5))
                note = null
            }
        }
        return b.build()
    }

    private fun rate(bps: Double): String {
        val units = arrayOf("B/s", "KB/s", "MB/s", "GB/s")
        var v = bps
        var i = 0
        while (v >= 1000 && i < units.size - 1) {
            v /= 1024
            i++
        }
        return if (i == 0 || v >= 100) "${v.toInt()} ${units[i]}" else String.format(java.util.Locale.US, "%.1f %s", v, units[i])
    }

    private fun shutdown() {
        cancelResume()
        main.removeCallbacks(tick)
        Mobile.call("vpn.stop", "{}")
        Xtune.setRunning(this, false)
        tun?.close()
        tun = null
        if (Build.VERSION.SDK_INT >= 24) stopForeground(STOP_FOREGROUND_REMOVE) else @Suppress("DEPRECATION") stopForeground(true)
        stopSelf()
    }

    override fun onRevoke() {
        worker.execute { shutdown() }
    }

    override fun onDestroy() {
        cancelResume()
        main.removeCallbacks(tick)
        Mobile.call("vpn.stop", "{}")
        Xtune.setRunning(this, false)
        tun?.close()
        tun = null
        worker.shutdown()
        super.onDestroy()
    }
}

/** The notification's words, in the app's language (which the app passes; not the phone's). */
class Strings(private val fa: Boolean) {
    companion object {
        fun of(c: Context) = Strings(Xtune.prefs(c).getString("lang", "en") == "fa")
    }

    val connected get() = if (fa) "متصل" else "Connected"
    val connecting get() = if (fa) "در حال اتصال…" else "Connecting…"
    val disconnect get() = if (fa) "قطع" else "Disconnect"
    val next get() = if (fa) "کانفیگ بعدی" else "Next config"
    val pause get() = if (fa) "توقف ۱۵ دقیقه" else "Pause 15 min"
    val paused get() = if (fa) "متوقف" else "Paused"
    val resume get() = if (fa) "اتصال دوباره" else "Resume now"
    val auto get() = if (fa) "خودکار" else "Auto"
    fun reconnectsAt(at: String) = if (fa) "ساعت $at خودش وصل می‌شود" else "Reconnects by itself at $at"
}
