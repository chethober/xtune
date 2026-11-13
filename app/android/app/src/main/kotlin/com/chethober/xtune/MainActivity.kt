package com.chethober.xtune

import android.app.Activity
import android.content.Intent
import android.content.pm.PackageManager
import android.net.VpnService
import android.os.Build
import io.flutter.embedding.android.FlutterActivity
import io.flutter.embedding.engine.FlutterEngine
import io.flutter.plugin.common.MethodChannel
import mobile.Mobile
import org.json.JSONObject
import kotlin.concurrent.thread

class MainActivity : FlutterActivity() {
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
                    thread {
                        val reply = Mobile.call(method, args)
                        runOnUiThread { result.success(reply) }
                    }
                }
                "startVpn" -> {
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
                    try {
                        startActivity(Intent(android.provider.Settings.ACTION_VPN_SETTINGS))
                    } catch (_: Exception) {
                        startActivity(Intent(android.provider.Settings.ACTION_WIRELESS_SETTINGS))
                    }
                    result.success(null)
                }
                "setLang" -> {
                    Xtune.prefs(applicationContext).edit().putString("lang", call.argument<String>("lang") ?: "en").apply()
                    result.success(null)
                }
                "paused" -> result.success(XtuneVpnService.pausedUntil)
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

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        intent.data?.let { channel?.invokeMethod("link", it.toString()) }
    }

    private fun startVpn() {
        val result = pendingResult ?: return
        pendingResult = null
        XtuneVpnService.onStarted = { reply ->
            runOnUiThread {
                result.success(reply)
                askNotifications() // after the VPN consent dialog, never on top of it
            }
        }
        XtuneVpnService.start(this, pendingArgs ?: "{}", pendingMode, pendingApps)
    }

    @Deprecated("Deprecated in Java")
    override fun onActivityResult(requestCode: Int, resultCode: Int, data: Intent?) {
        super.onActivityResult(requestCode, resultCode, data)
        if (requestCode != VPN_REQUEST) return
        if (resultCode == RESULT_OK) {
            startVpn()
        } else {
            pendingResult?.success("{\"ok\":false,\"error\":\"VPN permission was not given\"}")
            pendingResult = null
        }
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
