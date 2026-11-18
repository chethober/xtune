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
import java.util.concurrent.ConcurrentHashMap
import java.util.concurrent.Executors
import java.util.concurrent.RejectedExecutionException
import java.util.concurrent.atomic.AtomicLong

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

        /** Who waits for a start's reply (the app's Connect), by the number the start carries. */
        private val waiting = ConcurrentHashMap<Long, (String) -> Unit>()
        private val nextReq = AtomicLong(1)

        /** Paused, and when it reconnects (wall clock ms); 0 when not paused. */
        @Volatile
        var pausedUntil = 0L
            private set

        /** The service exists (connected, connecting or paused). */
        @Volatile
        var alive = false
            private set

        fun fail(msg: String): String = JSONObject().put("ok", false).put("error", msg).toString()

        /**
         * Starts the connection saved with Xtune.saveLast. The profile is not put in the Intent:
         * a large subscription is more than Android lets one Intent carry
         * (TransactionTooLargeException), and the service runs in this process anyway.
         * reason is what started it, for the timeline: boot, wifi, tile, widget; "" from the app.
         * onDone gets the core's reply. Returns null once asked, or why Android refused to start it.
         */
        fun start(context: Context, reason: String = "", onDone: ((String) -> Unit)? = null): String? {
            val req = if (onDone != null) nextReq.getAndIncrement().also { waiting[it] = onDone } else 0L
            val i = Intent(context, XtuneVpnService::class.java).setAction(ACTION_START)
                .putExtra("reason", reason).putExtra("req", req)
            return try {
                if (Build.VERSION.SDK_INT >= 26) context.startForegroundService(i) else context.startService(i)
                null
            } catch (e: Exception) {
                // Android 12+ refuses a foreground service started from the background
                waiting.remove(req)
                CrashLog.write(context, "error", "start the VPN ($reason)", e)
                e.message ?: e.toString()
            }
        }

        fun stop(context: Context) = send(context, ACTION_STOP)

        /** Sends an action to the running service; false when Android refused (it is not running). */
        fun send(context: Context, action: String): Boolean = try {
            context.startService(Intent(context, XtuneVpnService::class.java).setAction(action))
            true
        } catch (e: Exception) {
            false
        }

        private fun reply(req: Long, msg: String) {
            waiting.remove(req)?.invoke(msg)
        }
    }

    @Volatile
    private var tun: ParcelFileDescriptor? = null
    @Volatile
    private var destroyed = false
    private val worker = Executors.newSingleThreadExecutor() // the core's calls, in order
    private val main = Handler(Looper.getMainLooper())
    private var status = JSONObject()
    private var lastUp = -1L
    private var lastDown = -1L
    private var lastAt = 0L
    private var upRate = 0.0
    private var downRate = 0.0
    private var note: String? = null // a passing message ("No other config"), shown once

    /** Runs f on the worker, after what is queued. An error in it is written down, not fatal. */
    private fun run(where: String, f: () -> Unit) {
        if (destroyed) return
        try {
            worker.execute { CrashLog.guard(this@XtuneVpnService, where) { f() } }
        } catch (_: RejectedExecutionException) {
            // the service is going away
        }
    }

    private val tick = object : Runnable {
        override fun run() {
            if (destroyed) return
            val screenOn = getSystemService(PowerManager::class.java)?.isInteractive ?: true
            if (tun != null && pausedUntil == 0L && screenOn) run("status") { refresh() }
            main.postDelayed(this, TICK_MS)
        }
    }

    private val resumeLater = Runnable { resume() }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        alive = true
        when (intent?.action) {
            ACTION_STOP -> {
                run("disconnect") { shutdown() }
                return START_NOT_STICKY
            }
            ACTION_NEXT -> {
                run("next config") { next() }
                return START_NOT_STICKY
            }
            ACTION_PAUSE -> {
                run("pause") { pause() }
                return START_NOT_STICKY
            }
        }
        val req = intent?.getLongExtra("req", 0L) ?: 0L
        // no action: the system started it (always-on VPN)
        val reason = when (intent?.action) {
            ACTION_RESUME -> "resume"
            ACTION_START -> intent.getStringExtra("reason") ?: ""
            else -> "always-on"
        }
        if (!foreground(connecting = true)) {
            // Android refused a start from the background: nothing is queued that could run on a
            // stopped service; the user is asked to connect instead
            reply(req, fail("Android did not let xtune connect from the background"))
            if (reason.isNotEmpty()) Xtune.askToConnect(this, reason)
            stopSelf()
            return START_NOT_STICKY
        }
        run("connect") { begin(reason, req) }
        return START_NOT_STICKY
    }

    /** Connects with the saved connection (the app's, or the last one: always-on VPN, Resume, the tile). */
    private fun begin(reason: String, req: Long) {
        var out = fail("not connected")
        try {
            if (destroyed) {
                out = fail("the VPN service stopped")
                return
            }
            cancelResume()
            val last = Xtune.loadLast(this)
            if (last == null) {
                out = fail("Nothing to connect yet: open xtune and connect once")
                shutdown()
                return
            }
            out = try {
                Xtune.setup(this)
                connect(last.args, last.mode, last.apps, reason)
            } catch (e: Exception) {
                fail(e.message ?: e.toString())
            }
            if (JSONObject(out).optBoolean("ok") && !destroyed) {
                Xtune.setRunning(this, true)
                lastUp = -1
                refresh()
                main.removeCallbacks(tick)
                main.postDelayed(tick, TICK_MS)
            } else {
                shutdown()
            }
        } finally {
            reply(req, out)
        }
    }

    private fun connect(args: String, mode: String, apps: List<String>, reason: String): String {
        Mobile.call("vpn.stop", "{\"reason\":\"restart\"}")
        tun?.close()
        tun = null
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
        var allowed = 0
        if (mode == "only") {
            for (p in chosen) {
                try {
                    b.addAllowedApplication(p)
                    allowed++
                } catch (_: Exception) {
                    // uninstalled since it was picked
                }
            }
        }
        if (allowed == 0) {
            // also "only" with none of its apps installed any more: an empty allow list would put
            // every app in the VPN, this one too, and the core's own traffic would loop into it
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
        if (tun == null) {
            if (pausedUntil == 0L) shutdown() // a stale notification of a service that is gone
            return
        }
        if (pausedUntil != 0L) return
        val r = JSONObject(Mobile.call("vpn.next", "{}"))
        if (!r.optBoolean("ok")) {
            note = r.optString("error").ifEmpty { null }
        } else {
            Xtune.setSelected(this, r.optJSONObject("result")?.optString("selected") ?: "")
        }
        lastUp = -1
        refresh()
    }

    /** Closes the tunnel for 15 minutes: traffic goes direct, then it reconnects by itself. */
    private fun pause() {
        if (tun == null) {
            if (pausedUntil == 0L) shutdown()
            return
        }
        Mobile.call("vpn.stop", "{\"reason\":\"pause\"}")
        tun?.close()
        tun = null
        pausedUntil = System.currentTimeMillis() + PAUSE_MS
        Xtune.setRunning(this, false)
        main.removeCallbacks(tick)
        // a Handler alone stops while the phone sleeps; the alarm wakes it (within minutes in Doze).
        // The alarm goes to a receiver, not straight to this service: Android 12+ does not let an
        // inexact alarm start a foreground service, and refusing one would close the app.
        main.postDelayed(resumeLater, PAUSE_MS)
        getSystemService(AlarmManager::class.java)?.setAndAllowWhileIdle(
            AlarmManager.ELAPSED_REALTIME_WAKEUP, SystemClock.elapsedRealtime() + PAUSE_MS, resumeIntent()
        )
        show()
    }

    /** The pause is over (the Handler, or the alarm through ResumeReceiver). */
    private fun resume() {
        if (destroyed || pausedUntil == 0L) return
        pausedUntil = 0L
        show(connecting = true)
        run("resume") { begin("resume", 0L) }
    }

    private fun resumeIntent(): PendingIntent = PendingIntent.getBroadcast(
        this, 4, Intent(this, ResumeReceiver::class.java),
        PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT
    )

    private fun cancelResume() {
        pausedUntil = 0L
        main.removeCallbacks(resumeLater)
        try {
            getSystemService(AlarmManager::class.java)?.cancel(resumeIntent())
        } catch (_: Exception) {
        }
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
        if (!destroyed) show()
    }

    private fun channel() {
        if (Build.VERSION.SDK_INT >= 26) {
            val nm = getSystemService(NotificationManager::class.java)
            val ch = NotificationChannel(CHANNEL, "VPN", NotificationManager.IMPORTANCE_LOW)
            ch.setShowBadge(false)
            nm.createNotificationChannel(ch)
        }
    }

    /** Becomes a foreground service; false when Android refused (a start from the background). */
    private fun foreground(connecting: Boolean): Boolean {
        return try {
            channel()
            val n = build(connecting)
            if (Build.VERSION.SDK_INT >= 34) {
                startForeground(NOTIFICATION, n, ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE)
            } else {
                startForeground(NOTIFICATION, n)
            }
            true
        } catch (e: Exception) {
            CrashLog.write(this, "error", "become a foreground service", e)
            false
        }
    }

    private fun show(connecting: Boolean = false) {
        try {
            getSystemService(NotificationManager::class.java).notify(NOTIFICATION, build(connecting))
        } catch (_: Exception) {
        }
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
            connecting || tun == null -> b.setContentTitle(s.connecting).addAction(stop)
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

    /** On the worker: disconnects and ends the service. */
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
        run("revoked") { shutdown() }
    }

    override fun onDestroy() {
        destroyed = true
        alive = false
        main.removeCallbacks(tick)
        main.removeCallbacks(resumeLater)
        try {
            getSystemService(AlarmManager::class.java)?.cancel(resumeIntent())
        } catch (_: Exception) {
        }
        pausedUntil = 0L
        // The core's stop can take a while (it waits for a reconnect in progress): never on the
        // main thread, where it would freeze the app. It runs after whatever is queued, and closes
        // a tunnel a connect still running opens.
        try {
            worker.execute {
                CrashLog.guard(this@XtuneVpnService, "stop on destroy") {
                    Mobile.call("vpn.stop", "{}")
                    tun?.close()
                    tun = null
                    Xtune.setRunning(this@XtuneVpnService, false)
                }
            }
        } catch (_: RejectedExecutionException) {
        }
        worker.shutdown()
        super.onDestroy()
    }
}

/** The pause's alarm: resumes the paused service, or asks the user when it is gone. */
class ResumeReceiver : android.content.BroadcastReceiver() {
    override fun onReceive(c: Context, intent: Intent) {
        // a running foreground service may be sent to from the background; a stopped one may not
        if (!XtuneVpnService.alive || !XtuneVpnService.send(c, XtuneVpnService.ACTION_RESUME)) {
            Xtune.askToConnect(c, "resume")
        }
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
