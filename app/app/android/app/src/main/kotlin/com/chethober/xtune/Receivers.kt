package com.chethober.xtune

import android.app.PendingIntent
import android.appwidget.AppWidgetManager
import android.appwidget.AppWidgetProvider
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.graphics.drawable.Icon
import android.os.Build
import android.service.quicksettings.Tile
import android.service.quicksettings.TileService
import android.widget.RemoteViews

/** Connects at boot (when the user asked for it) and re-arms the Wi-Fi watch. */
class BootReceiver : BroadcastReceiver() {
    override fun onReceive(c: Context, intent: Intent) {
        if (intent.action != Intent.ACTION_BOOT_COMPLETED && intent.action != Intent.ACTION_MY_PACKAGE_REPLACED) return
        CrashLog.guard(c, "boot") {
            val p = Xtune.prefs(c)
            if (p.getBoolean("autoWifi", false)) Xtune.watchWifi(c, true)
            if (intent.action == Intent.ACTION_BOOT_COMPLETED && p.getBoolean("autoBoot", false)) Xtune.startLast(c, "boot")
        }
    }
}

/** A Wi-Fi network came up: connect unless it is a trusted one. */
class WifiReceiver : BroadcastReceiver() {
    override fun onReceive(c: Context, intent: Intent) = CrashLog.guard(c, "Wi-Fi auto-connect") { Xtune.onWifi(c) }
}

/** The Quick Settings tile: tap to connect or disconnect. */
class XtuneTileService : TileService() {
    override fun onStartListening() {
        val t = qsTile ?: return
        t.state = if (Xtune.running) Tile.STATE_ACTIVE else Tile.STATE_INACTIVE
        t.label = "xtune"
        if (Build.VERSION.SDK_INT >= 29) t.subtitle = if (Xtune.running) "Connected" else "Off"
        t.icon = Icon.createWithResource(this, R.drawable.ic_vpn)
        t.updateTile()
    }

    override fun onClick() {
        if (!Xtune.running && !Xtune.hasLast(this) || isLocked && !Xtune.running) {
            val i = Intent(this, MainActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
            if (Build.VERSION.SDK_INT >= 34) {
                startActivityAndCollapse(PendingIntent.getActivity(this, 0, i, PendingIntent.FLAG_IMMUTABLE))
            } else {
                @Suppress("DEPRECATION", "StartActivityAndCollapseDeprecated")
                startActivityAndCollapse(i)
            }
            return
        }
        CrashLog.guard(this, "tile") { Xtune.toggle(this, "tile") }
        onStartListening()
    }
}

/** The home-screen widget: the connect button and whether it is on. */
class XtuneWidget : AppWidgetProvider() {
    companion object {
        const val TOGGLE = "com.chethober.xtune.TOGGLE"

        fun update(c: Context, wm: AppWidgetManager, ids: IntArray) {
            val on = Xtune.running
            val v = RemoteViews(c.packageName, R.layout.widget)
            v.setTextViewText(R.id.widget_status, if (on) "Connected" else "Not connected")
            v.setInt(R.id.widget_button, "setBackgroundResource", if (on) R.drawable.widget_button_on else R.drawable.widget_button_off)
            v.setInt(R.id.widget_icon, "setColorFilter", if (on) 0xFF090C12.toInt() else 0xFF9BA7B4.toInt())
            val pi = PendingIntent.getBroadcast(
                c, 0, Intent(c, XtuneWidget::class.java).setAction(TOGGLE),
                PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT
            )
            v.setOnClickPendingIntent(R.id.widget_root, pi)
            wm.updateAppWidget(ids, v)
        }
    }

    override fun onUpdate(c: Context, wm: AppWidgetManager, ids: IntArray) = update(c, wm, ids)

    override fun onReceive(c: Context, intent: Intent) {
        super.onReceive(c, intent)
        if (intent.action == TOGGLE) CrashLog.guard(c, "widget") { Xtune.toggle(c, "widget") }
    }
}

/**
 * The server widget: a server the app looked at in Manage, as it was then (the app keeps it
 * current while it runs; logging in to the server from the background would need its password
 * outside the app's secure storage). With more than one server, the arrow shows the next.
 */
class XtuneServerWidget : AppWidgetProvider() {
    companion object {
        const val NEXT = "com.chethober.xtune.NEXT_SERVER"

        /** One server's numbers, by its ID in the app. */
        fun save(c: Context, id: String, json: String) {
            val p = Xtune.prefs(c)
            val all = try {
                org.json.JSONObject(p.getString("serverWidgets", "{}") ?: "{}")
            } catch (_: Exception) {
                org.json.JSONObject()
            }
            all.put(id.ifEmpty { "_" }, org.json.JSONObject(json))
            p.edit().putString("serverWidgets", all.toString()).putString("serverWidgetShown", id.ifEmpty { "_" }).remove("serverWidget").apply()
            refresh(c)
        }

        /** Forgets the servers the app no longer has. */
        fun keep(c: Context, ids: List<String>) {
            val p = Xtune.prefs(c)
            val all = try {
                org.json.JSONObject(p.getString("serverWidgets", "{}") ?: "{}")
            } catch (_: Exception) {
                return
            }
            for (k in all.keys().asSequence().toList()) if (k !in ids) all.remove(k)
            p.edit().putString("serverWidgets", all.toString()).apply()
            refresh(c)
        }

        fun refresh(c: Context) {
            try {
                val wm: AppWidgetManager? = AppWidgetManager.getInstance(c)
                val ids = wm?.getAppWidgetIds(android.content.ComponentName(c, XtuneServerWidget::class.java)) ?: IntArray(0)
                if (wm != null && ids.isNotEmpty()) update(c, wm, ids)
            } catch (_: Exception) {
            }
        }

        /** The servers' numbers, in the order the app saved them, and which one is shown. */
        private fun servers(c: Context): Pair<List<Pair<String, org.json.JSONObject>>, Int> {
            val p = Xtune.prefs(c)
            val out = mutableListOf<Pair<String, org.json.JSONObject>>()
            try {
                val all = org.json.JSONObject(p.getString("serverWidgets", "{}") ?: "{}")
                for (k in all.keys()) all.optJSONObject(k)?.let { out.add(k to it) }
            } catch (_: Exception) {
            }
            if (out.isEmpty()) {
                // 1.5 kept one server
                p.getString("serverWidget", null)?.let { raw ->
                    try {
                        out.add("_" to org.json.JSONObject(raw))
                    } catch (_: Exception) {
                    }
                }
            }
            val shown = p.getString("serverWidgetShown", null)
            return out to out.indexOfFirst { it.first == shown }.coerceAtLeast(0)
        }

        fun next(c: Context) {
            val (list, i) = servers(c)
            if (list.size < 2) return
            Xtune.prefs(c).edit().putString("serverWidgetShown", list[(i + 1) % list.size].first).apply()
            refresh(c)
        }

        fun update(c: Context, wm: AppWidgetManager, ids: IntArray) {
            val v = RemoteViews(c.packageName, R.layout.server_widget)
            val fa = Xtune.prefs(c).getString("lang", "en") == "fa"
            val (list, i) = servers(c)
            if (list.isEmpty()) {
                v.setTextViewText(R.id.srv_name, if (fa) "سروری باز نشده" else "Open a server in xtune")
                v.setViewVisibility(R.id.srv_next, android.view.View.GONE)
            } else {
                val j = list[i].second
                v.setTextViewText(R.id.srv_name, j.optString("name"))
                v.setTextViewText(R.id.srv_online, "${j.optInt("online")}/${j.optInt("users")}")
                v.setTextViewText(R.id.srv_today, j.optString("today"))
                v.setTextViewText(R.id.srv_configs, j.optString("configs"))
                v.setTextViewText(R.id.srv_online_label, if (fa) "آنلاین" else "online")
                v.setTextViewText(R.id.srv_today_label, if (fa) "امروز" else "today")
                val at = j.optLong("at")
                val time = if (at > 0) java.text.DateFormat.getTimeInstance(java.text.DateFormat.SHORT).format(java.util.Date(at)) else ""
                v.setTextViewText(R.id.srv_when, if (list.size > 1) "$time · ${i + 1}/${list.size}" else time)
                v.setImageViewResource(R.id.srv_dot, if (j.optBoolean("ok", true)) R.drawable.widget_button_on else R.drawable.widget_button_off)
                v.setViewVisibility(R.id.srv_next, if (list.size > 1) android.view.View.VISIBLE else android.view.View.GONE)
                val next = PendingIntent.getBroadcast(
                    c, 13, Intent(c, XtuneServerWidget::class.java).setAction(NEXT),
                    PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT
                )
                v.setOnClickPendingIntent(R.id.srv_next, next)
            }
            val open = PendingIntent.getActivity(
                c, 11, Intent(c, MainActivity::class.java).setData(android.net.Uri.parse("xtune://manage")),
                PendingIntent.FLAG_IMMUTABLE
            )
            v.setOnClickPendingIntent(R.id.srv_root, open)
            wm.updateAppWidget(ids, v)
        }
    }

    override fun onUpdate(c: Context, wm: AppWidgetManager, ids: IntArray) = update(c, wm, ids)

    override fun onReceive(c: Context, intent: Intent) {
        super.onReceive(c, intent)
        if (intent.action == NEXT) CrashLog.guard(c, "server widget") { next(c) }
    }
}
