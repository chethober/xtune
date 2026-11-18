package com.chethober.xtune

import android.app.Activity
import android.content.Intent
import android.content.pm.PackageManager
import android.net.VpnService
import android.os.Build
import io.flutter.embedding.android.FlutterFragmentActivity
import io.flutter.embedding.engine.FlutterEngine
import io.flutter.plugin.common.MethodChannel
import mobile.Mobile
import org.json.JSONObject
import kotlin.concurrent.thread

class MainActivity : FlutterFragmentActivity() {
    private var pendingResult: MethodChannel.Result? = null
    private var pendingArgs: String? = null
    private var pendingMode: String = "off"
    private var pendingApps: ArrayList<String> = arrayListOf()
    private var channel: MethodChannel? = null

    companion object {
        private const val VPN_REQUEST = 7001
        private const val LOCATION_REQUEST = 7002
        private const val NOTIFY_REQUEST = 7004
    }

    private var locationResult: MethodChannel.Result? = null

    // The core's calls: a pool, not a new thread for each. Status polls every second would otherwise
    // pile up threads while a connect holds the core's lock.
    private val calls = java.util.concurrent.Executors.newCachedThreadPool()

    override fun configureFlutterEngine(flutterEngine: FlutterEngine) {
        super.configureFlutterEngine(flutterEngine)
        thread { Xtune.setup(applicationContext) }
        channel = MethodChannel(flutterEngine.dartExecutor.binaryMessenger, "xtune/core")
        channel!!.setMethodCallHandler { call, result ->
            when (call.method) {
                "initialLink" -> result.success(intent?.data?.toString())
                "call" -> {
                    val method = call.argument<String>("method") ?: ""
                    val args = call.argument<String>("args") ?: "{}"
                    calls.execute {
                        val reply = try {
                            Mobile.call(method, args)
                        } catch (e: Throwable) {
                            XtuneVpnService.fail(e.message ?: e.toString())
                        }
                        runOnUiThread { result.success(reply) }
                    }
                }
                "startVpn" -> {
                    // a Connect still waiting for the consent dialog: answer it before replacing it
                    pendingResult?.success(XtuneVpnService.fail("replaced by a newer connect"))
                    pendingArgs = call.argument<String>("args")
                    pendingMode = call.argument<String>("perAppMode") ?: "off"
                    pendingApps = ArrayList(call.argument<List<String>>("apps") ?: emptyList())
                    pendingResult = result
                    val consent = VpnService.prepare(this)
                    if (consent != null) {
                        startActivityForResult(consent, VPN_REQUEST)
                    } else {
                        startVpn()
                    }
                }
                "setAuto" -> {
                    Xtune.setAuto(
                        applicationContext,
                        call.argument<Boolean>("boot") ?: false,
                        call.argument<Boolean>("wifi") ?: false,
                        call.argument<List<String>>("trusted") ?: emptyList()
                    )
                    result.success(null)
                }
                "askLocation" -> askLocation(result)
                "vpnSettings" -> {
                    openFirst(Intent(android.provider.Settings.ACTION_VPN_SETTINGS), Intent(android.provider.Settings.ACTION_WIRELESS_SETTINGS))
                    result.success(null)
                }
                "batteryExempt" -> result.success(Xtune.batteryExempt(applicationContext))
                "askBattery" -> {
                    // Android's own dialog: "Let xtune always run in the background?"
                    @android.annotation.SuppressLint("BatteryLife")
                    val ask = Intent(android.provider.Settings.ACTION_REQUEST_IGNORE_BATTERY_OPTIMIZATIONS, android.net.Uri.parse("package:$packageName"))
                    openFirst(ask, Intent(android.provider.Settings.ACTION_IGNORE_BATTERY_OPTIMIZATION_SETTINGS))
                    result.success(null)
                }
                "testCrash" -> {
                    // debug builds only: checks that the crash log catches a crash
                    if ((applicationInfo.flags and android.content.pm.ApplicationInfo.FLAG_DEBUGGABLE) != 0) main().post { throw RuntimeException("test crash from Settings") }
                    result.success(null)
                }
                "setLang" -> {
                    Xtune.prefs(applicationContext).edit().putString("lang", call.argument<String>("lang") ?: "en").apply()
                    result.success(null)
                }
                "paused" -> result.success(XtuneVpnService.pausedUntil)
                "captive" -> result.success(Xtune.captive(applicationContext))
                "abi" -> result.success(Build.SUPPORTED_ABIS.firstOrNull() ?: "arm64-v8a")
                "installApk" -> result.success(installApk(call.argument<String>("path") ?: ""))
                "metered" -> result.success(
                    getSystemService(android.net.ConnectivityManager::class.java)?.isActiveNetworkMetered ?: false
                )
                "serverWidget" -> {
                    XtuneServerWidget.save(applicationContext, call.argument<String>("id") ?: "", call.argument<String>("json") ?: "{}")
                    result.success(null)
                }
                "serverWidgetKeep" -> {
                    XtuneServerWidget.keep(applicationContext, call.argument<List<String>>("ids") ?: emptyList())
                    result.success(null)
                }
                "stopVpn" -> {
                    XtuneVpnService.stop(this)
                    result.success(null)
                }
                "apps" -> thread {
                    val apps = installedApps()
                    runOnUiThread { result.success(apps) }
                }
                else -> result.notImplemented()
            }
        }
    }

    private fun main() = android.os.Handler(android.os.Looper.getMainLooper())

    /** Opens the first of these settings pages the phone has. */
    private fun openFirst(vararg pages: Intent) {
        for (i in pages) {
            try {
                startActivity(i)
                return
            } catch (_: Exception) {
            }
        }
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        intent.data?.let { channel?.invokeMethod("link", it.toString()) }
    }

    private fun startVpn() {
        val result = pendingResult ?: return
        pendingResult = null
        val once = Once(result)
        try {
            Xtune.saveLast(applicationContext, pendingArgs ?: "{}", pendingMode, pendingApps)
        } catch (e: Exception) {
            once.success(XtuneVpnService.fail("could not save the connection: ${e.message}"))
            return
        }
        val refused = XtuneVpnService.start(applicationContext) { reply ->
            runOnUiThread {
                once.success(reply)
                askNotifications() // after the VPN consent dialog, never on top of it
            }
        }
        if (refused != null) once.success(XtuneVpnService.fail("Android did not start the VPN: $refused"))
    }

    /** A method call's reply, sent at most once: a second reply would close the app. */
    private class Once(private val r: MethodChannel.Result) {
        private val done = java.util.concurrent.atomic.AtomicBoolean(false)
        fun success(v: Any?) {
            if (done.compareAndSet(false, true)) r.success(v)
        }
    }

    @Deprecated("Deprecated in Java")
    override fun onActivityResult(requestCode: Int, resultCode: Int, data: Intent?) {
        super.onActivityResult(requestCode, resultCode, data)
        if (requestCode != VPN_REQUEST) return
        if (resultCode == RESULT_OK) {
            try {
                startVpn()
            } catch (e: Exception) {
                CrashLog.write(this, "error", "start the VPN after consent", e)
                pendingResult?.success(XtuneVpnService.fail(e.message ?: e.toString()))
                pendingResult = null
            }
        } else {
            pendingResult?.success("{\"ok\":false,\"error\":\"VPN permission was not given\"}")
            pendingResult = null
        }
    }

    /**
     * Hands a downloaded update to Android's installer, which always asks. The first time, Android
     * wants this app allowed to install apps: its settings page opens instead, and "settings" is
     * returned so the app can say why.
     */
    private fun installApk(path: String): String {
        val f = java.io.File(path)
        if (!f.exists()) return "missing"
        if (Build.VERSION.SDK_INT >= 26 && !packageManager.canRequestPackageInstalls()) {
            startActivity(
                Intent(android.provider.Settings.ACTION_MANAGE_UNKNOWN_APP_SOURCES, android.net.Uri.parse("package:$packageName"))
            )
            return "settings"
        }
        val uri = androidx.core.content.FileProvider.getUriForFile(this, "$packageName.updates", f)
        startActivity(
            Intent(Intent.ACTION_VIEW).setDataAndType(uri, "application/vnd.android.package-archive")
                .addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION or Intent.FLAG_ACTIVITY_NEW_TASK)
        )
        return "ok"
    }

    /** The connection's notification (its speeds and buttons) needs this on Android 13 and later. */
    private fun askNotifications() {
        if (Build.VERSION.SDK_INT >= 33 &&
            checkSelfPermission(android.Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED
        ) {
            requestPermissions(arrayOf(android.Manifest.permission.POST_NOTIFICATIONS), NOTIFY_REQUEST)
        }
    }

    /** Reading Wi-Fi names needs the location permission ("all the time" for it to work in the background). */
    private fun askLocation(result: MethodChannel.Result) {
        val fine = android.Manifest.permission.ACCESS_FINE_LOCATION
        if (checkSelfPermission(fine) == PackageManager.PERMISSION_GRANTED) {
            if (Build.VERSION.SDK_INT >= 29 &&
                checkSelfPermission(android.Manifest.permission.ACCESS_BACKGROUND_LOCATION) != PackageManager.PERMISSION_GRANTED
            ) {
                requestPermissions(arrayOf(android.Manifest.permission.ACCESS_BACKGROUND_LOCATION), LOCATION_REQUEST + 1)
            }
            result.success(true)
            return
        }
        locationResult = result
        requestPermissions(arrayOf(fine), LOCATION_REQUEST)
    }

    override fun onRequestPermissionsResult(requestCode: Int, permissions: Array<out String>, grantResults: IntArray) {
        super.onRequestPermissionsResult(requestCode, permissions, grantResults)
        if (requestCode == LOCATION_REQUEST) {
            val ok = grantResults.isNotEmpty() && grantResults[0] == PackageManager.PERMISSION_GRANTED
            locationResult?.success(ok)
            locationResult = null
            if (ok && Build.VERSION.SDK_INT >= 29) {
                requestPermissions(arrayOf(android.Manifest.permission.ACCESS_BACKGROUND_LOCATION), LOCATION_REQUEST + 1)
            }
        }
    }

    private fun installedApps(): List<Map<String, String>> {
        val pm = packageManager
        val intent = Intent(Intent.ACTION_MAIN).addCategory(Intent.CATEGORY_LAUNCHER)
        @Suppress("DEPRECATION")
        return pm.queryIntentActivities(intent, 0)
            .map { it.activityInfo.applicationInfo }
            .distinctBy { it.packageName }
            .filter { it.packageName != packageName }
            .map { mapOf("package" to it.packageName, "label" to pm.getApplicationLabel(it).toString()) }
            .sortedBy { it["label"]?.lowercase() }
    }
}
