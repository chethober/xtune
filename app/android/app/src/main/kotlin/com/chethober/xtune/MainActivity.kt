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
import java.io.File
import kotlin.concurrent.thread

class MainActivity : FlutterActivity() {
    private var pendingResult: MethodChannel.Result? = null
    private var pendingArgs: String? = null
    private var pendingMode: String = "off"
    private var pendingApps: ArrayList<String> = arrayListOf()
    private var channel: MethodChannel? = null

    companion object {
        private const val VPN_REQUEST = 7001

        /** Copies the geo databases out of the APK once per app version and tells the core where they are. */
        fun setupCore(activity: Activity) {
            val dir = File(activity.filesDir, "geo")
            val stamp = File(dir, ".version")
            val version = try {
                activity.packageManager.getPackageInfo(activity.packageName, 0).let {
                    if (Build.VERSION.SDK_INT >= 28) it.longVersionCode.toString() else @Suppress("DEPRECATION") it.versionCode.toString()
                }
            } catch (e: PackageManager.NameNotFoundException) {
                "0"
            }
            if (!stamp.exists() || stamp.readText() != version) {
                dir.mkdirs()
                for (name in listOf("geoip.dat", "geosite.dat")) {
                    try {
                        activity.assets.open("geo/$name").use { input ->
                            File(dir, name).outputStream().use { input.copyTo(it) }
                        }
                    } catch (_: Exception) {
                    }
                }
                stamp.writeText(version)
            }
            Mobile.call("setup", JSONObject().put("assetDir", dir.absolutePath).toString())
        }
    }

    override fun configureFlutterEngine(flutterEngine: FlutterEngine) {
        super.configureFlutterEngine(flutterEngine)
        thread { setupCore(this) }
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
        XtuneVpnService.onStarted = { reply -> runOnUiThread { result.success(reply) } }
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
