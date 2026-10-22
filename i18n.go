package main

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

// The subscription page speaks English or Persian. Persian pages are right-to-left, use the
// Solar Hijri (Jalali) calendar and Persian digits.

var langs = []string{"en", "fa"}

var pageText = map[string]map[string]string{
	"en": {
		"title": "subscription", "used": "Used", "quota": "Quota", "expires": "Expires", "devices": "Devices",
		"resets": "Usage resets", "last": "Last connected", "never": "never", "unlimited": "unlimited",
		"online": "%d online", "onlineOf": "%d of %d online", "left": "%s (%d days left)", "expired": "expired %s",
		"active": "active", "disabled": "disabled", "over quota": "over quota",
		"subLink": "Subscription link", "copy": "Copy", "copied": "Copied", "qrAlt": "QR code of the subscription link",
		"subHelp":  "Add it as a subscription in v2rayN, v2rayNG, Hiddify, Streisand or Happ. The app keeps your configs up to date.",
		"failover": "Automatic failover config",
		"failHelp": "One Xray config with every server below. It keeps using the fastest one that works, so you switch automatically when one is blocked. Import it as a custom config (v2rayN: Add custom config).",
		"single":   "Single configs", "monthly": "monthly, next on %s", "weekly": "weekly, next on %s",
		"history": "Last 30 days",
		"files":   "Other apps", "download": "Download",
		"filesHelp": "For OpenVPN, IKEv2 (strongSwan on Android; iPhone, iPad and Mac open the profile directly), the WireGuard app and SSH or DNS tunnel apps (HTTP Injector, NetMod). They log in with the user name and password above.",
	},
	"fa": {
		"title": "اشتراک", "used": "مصرف‌شده", "quota": "سهمیه", "expires": "انقضا", "devices": "دستگاه‌ها",
		"resets": "صفر شدن مصرف", "last": "آخرین اتصال", "never": "هرگز", "unlimited": "نامحدود",
		"online": "%d آنلاین", "onlineOf": "%d از %d آنلاین", "left": "%s (%d روز مانده)", "expired": "منقضی‌شده در %s",
		"active": "فعال", "disabled": "غیرفعال", "over quota": "سهمیه تمام شده",
		"subLink": "لینک اشتراک", "copy": "کپی", "copied": "کپی شد", "qrAlt": "کد QR لینک اشتراک",
		"subHelp":  "این لینک را در v2rayN، v2rayNG، Hiddify، Streisand یا Happ به‌عنوان اشتراک (Subscription) اضافه کنید. برنامه کانفیگ‌ها را خودکار به‌روز نگه می‌دارد.",
		"failover": "کانفیگ با جابه‌جایی خودکار",
		"failHelp": "یک کانفیگ Xray که همهٔ سرورهای زیر را دارد. همیشه سریع‌ترین سرورِ در دسترس را به کار می‌برد، پس اگر یکی مسدود شود خودکار سراغ بعدی می‌رود. آن را به‌عنوان کانفیگ سفارشی وارد کنید (در v2rayN: Add custom config).",
		"single":   "کانفیگ‌های تکی", "monthly": "ماهانه، بعدی در %s", "weekly": "هفتگی، بعدی در %s",
		"history": "۳۰ روز گذشته",
		"files":   "برنامه‌های دیگر", "download": "دانلود",
		"filesHelp": "برای OpenVPN، IKEv2 (در اندروید برنامهٔ strongSwan؛ آیفون، آیپد و مک پروفایل را مستقیم باز می‌کنند)، برنامهٔ WireGuard و برنامه‌های تونل SSH یا DNS (HTTP Injector، NetMod). ورود با نام کاربری و رمز بالا انجام می‌شود.",
	},
}

// pageLang picks the page language: ?lang=, then the server setting, then the browser's preference.
func pageLang(req *http.Request, setting string) string {
	if l := req.URL.Query().Get("lang"); pageText[l] != nil {
		return l
	}
	if pageText[setting] != nil {
		return setting
	}
	for _, part := range strings.Split(req.Header.Get("Accept-Language"), ",") {
		tag := strings.ToLower(strings.TrimSpace(strings.SplitN(part, ";", 2)[0]))
		if base, _, _ := strings.Cut(tag, "-"); pageText[base] != nil {
			return base
		}
	}
	return "en"
}

func validLang(l string) error {
	if l != "" && pageText[l] == nil {
		return fmt.Errorf("unknown language %q (en, fa, or auto)", l)
	}
	return nil
}

// fmtDate is a date in the page's calendar.
func fmtDate(t time.Time, lang string) string {
	if lang == "fa" {
		y, m, d := jalali(t.Year(), int(t.Month()), t.Day())
		return faDigits(fmt.Sprintf("%04d/%02d/%02d", y, m, d))
	}
	return t.Format("2006-01-02")
}

// jalali converts a Gregorian date to the Solar Hijri calendar used in Iran.
func jalali(gy, gm, gd int) (jy, jm, jd int) {
	before := []int{0, 31, 59, 90, 120, 151, 181, 212, 243, 273, 304, 334}
	gy2 := gy
	if gm > 2 {
		gy2 = gy + 1
	}
	days := 355666 + 365*gy + (gy2+3)/4 - (gy2+99)/100 + (gy2+399)/400 + gd + before[gm-1]
	jy = -1595 + 33*(days/12053)
	days %= 12053
	jy += 4 * (days / 1461)
	days %= 1461
	if days > 365 {
		jy += (days - 1) / 365
		days = (days - 1) % 365
	}
	if days < 186 {
		return jy, 1 + days/31, 1 + days%31
	}
	return jy, 7 + (days-186)/30, 1 + (days-186)%30
}

// faDigits writes ASCII digits (and the decimal point) as Persian ones.
func faDigits(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune('۰' + r - '0')
		case r == '.':
			b.WriteRune('٫')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
