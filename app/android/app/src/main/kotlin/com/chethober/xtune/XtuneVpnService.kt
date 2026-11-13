package com.chethober.xtune

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.net.VpnService
import android.os.Build
import android.os.ParcelFileDescriptor
import mobile.Mobile
import org.json.JSONObject
import kotlin.concurrent.thread

/**
 * The whole-device connection: Android hands over a TUN descriptor, and the Go core's Xray reads
 * every packet from it. This app is left out of the VPN, so the core's own connections to the
 * servers go out directly instead of looping back in.
 */
class XtuneVpnService : VpnService() {
    companion object {
        const val ACTION_START = "com.chethober.xtune.START"
        const val ACTION_STOP = "com.chethober.xtune.STOP"
        private const val CHANNEL = "vpn"
        private const val NOTIFICATION = 1

        @Volatile
        var onStarted: ((String) -> Unit)? = null

        fun start(context: Context, args: String, excluded: ArrayList<String>) {
            val i = Intent(context, XtuneVpnService::class.java).setAction(ACTION_START)
                .putExtra("args", args).putStringArrayListExtra("excluded", excluded)
            if (Build.VERSION.SDK_INT >= 26) context.startForegroundService(i) else context.startService(i)
        }

        fun stop(context: Context) {
            context.startService(Intent(context, XtuneVpnService::class.java).setAction(ACTION_STOP))
        }
    }

    private var tun: ParcelFileDescriptor? = null

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        if (intent?.action == ACTION_STOP) {
            shutdown()
            return START_NOT_STICKY
        }
        foreground()
        val args = intent?.getStringExtra("args") ?: "{}"
        val excluded = intent?.getStringArrayListExtra("excluded") ?: arrayListOf()
        thread {
            val reply = try {
                connect(args, excluded)
            } catch (e: Exception) {
                JSONObject().put("ok", false).put("error", e.message ?: e.toString()).toString()
            }
            if (!JSONObject(reply).optBoolean("ok")) shutdown()
            onStarted?.invoke(reply)
            onStarted = null
        }
        return START_NOT_STICKY
    }

    private fun connect(args: String, excluded: List<String>): String {
        Mobile.call("vpn.stop", "{}")
        tun?.close()
        val b = Builder()
            .setSession("xtune")
            .setMtu(1500)
            .addAddress("172.19.0.1", 30)
            .addRoute("0.0.0.0", 0)
            .addDnsServer("1.1.1.1")
        b.addDisallowedApplication(packageName)
        for (p in excluded) {
            try {
                b.addDisallowedApplication(p)
            } catch (_: Exception) {
            }
        }
        if (Build.VERSION.SDK_INT >= 29) b.setMetered(false)
        val pfd = b.establish() ?: throw IllegalStateException("Android did not create the VPN (permission revoked?)")
        tun = pfd
        val j = JSONObject(args)
        val opts = j.optJSONObject("options") ?: JSONObject()
        opts.put("mode", "tun").put("tunFd", pfd.fd).put("routes", false)
        j.put("options", opts)
        return Mobile.call("vpn.start", j.toString())
    }

    private fun foreground() {
        val nm = getSystemService(NotificationManager::class.java)
        if (Build.VERSION.SDK_INT >= 26) {
            nm.createNotificationChannel(NotificationChannel(CHANNEL, "VPN", NotificationManager.IMPORTANCE_LOW))
        }
        val open = PendingIntent.getActivity(
            this, 0, Intent(this, MainActivity::class.java), PendingIntent.FLAG_IMMUTABLE
        )
        val stop = PendingIntent.getService(
            this, 1, Intent(this, XtuneVpnService::class.java).setAction(ACTION_STOP), PendingIntent.FLAG_IMMUTABLE
        )
        val builder = if (Build.VERSION.SDK_INT >= 26) Notification.Builder(this, CHANNEL) else @Suppress("DEPRECATION") Notification.Builder(this)
        val n = builder
            .setSmallIcon(R.drawable.ic_vpn)
            .setContentTitle("xtune")
            .setContentText("Connected")
            .setContentIntent(open)
            .setOngoing(true)
            .addAction(Notification.Action.Builder(null, "Disconnect", stop).build())
            .build()
        if (Build.VERSION.SDK_INT >= 34) {
            startForeground(NOTIFICATION, n, ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE)
        } else {
            startForeground(NOTIFICATION, n)
        }
    }

    private fun shutdown() {
        Mobile.call("vpn.stop", "{}")
        tun?.close()
        tun = null
        if (Build.VERSION.SDK_INT >= 24) stopForeground(STOP_FOREGROUND_REMOVE) else @Suppress("DEPRECATION") stopForeground(true)
        stopSelf()
    }

    override fun onRevoke() {
        shutdown()
    }

    override fun onDestroy() {
        Mobile.call("vpn.stop", "{}")
        tun?.close()
        tun = null
        super.onDestroy()
    }
}
