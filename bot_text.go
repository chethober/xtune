package main

import "fmt"

// The bot speaks English or Persian (xtune bot lang fa). Commands stay in English: Telegram only
// allows Latin command names. Persian strings take their numbers as %s: tr writes them in Persian
// digits.

func tr(lang, key string, args ...any) string {
	s, ok := botText[lang][key]
	if !ok {
		s = botText["en"][key]
	}
	if len(args) == 0 {
		return s
	}
	if lang == "fa" {
		for i, a := range args {
			switch a.(type) {
			case int, int64:
				args[i] = faDigits(fmt.Sprint(a))
			}
		}
	}
	return fmt.Sprintf(s, args...)
}

var botText = map[string]map[string]string{
	"en": {
		"cmd_users": "List users", "cmd_user": "NAME: usage, limits and actions",
		"cmd_add": "NAME [PLAN | GB DAYS DEVICES]: add a user", "cmd_renew": "NAME [PLAN]: renew a user",
		"cmd_links": "NAME: links and QR code", "cmd_status": "Server status", "cmd_help": "All commands",

		"help": `<b>xtune</b>

/users - list users
/user NAME - usage, limits and actions
/add NAME [GB] [DAYS] [DEVICES] - add a user (0 = unlimited / never)
/add NAME PLAN - add a user with a plan's limits
/limit NAME quota=GB days=N devices=N reset=monthly|weekly|none
/renew NAME [PLAN] - extend expiry, usage from zero
/links NAME - subscription link, configs and QR code
/enable NAME, /disable NAME
/resetusage NAME - start their usage from zero
/rotate NAME - new keys (old links stop working)
/resetsub NAME - new subscription link
/del NAME - delete a user
/plans - plans
/status - server status`,
		"help_admin": `/resellers - resellers
/backup - an encrypted backup, now`,
		"help_examples": "Examples: <code>/add carol 50 30 2</code>, <code>/add dave basic</code>, <code>/limit carol quota=100 reset=monthly</code>",
		"help_user": `/me - your usage and expiry
/links - your configs and QR code
/unlink - stop showing your account here`,

		"err_unknown":        "unknown command /%s, see /help",
		"err_needs_name":     "/%s needs a user name: /%s NAME",
		"err_no_user":        "no user %q",
		"err_name_long":      "the name is too long for buttons: do this on the server (xtune users)",
		"err_limit_usage":    "pass what to change: /limit NAME quota=GB days=N devices=N reset=monthly|weekly|none",
		"err_res_users":      "you may have at most %d users",
		"err_res_unlimited":  "your users need a quota: add one, e.g. /add NAME 20 30",
		"err_res_quota":      "that is more quota than you have left (%s)",
		"err_no_selfservice": "user self-service is off (on the server: xtune bot self-service on)",
		"err_no_backup_pass": "no backup passphrase yet; on the server: xtune bot backup on",

		"confirm_rotate":   "Give %s new keys? Their current links stop working until they import the new ones.",
		"confirm_resetsub": "Give %s a new subscription link? The current one stops working.",
		"confirm_del":      "Delete %s? Their links stop working.",
		"added":            "Added %s.",
		"renewed":          "Renewed: expires %s, usage from zero.",
		"done_limits":      "Limits changed.",
		"done_enable":      "Enabled.",
		"done_disable":     "Disabled.",
		"done_resetusage":  "Usage reset.",
		"new_keys":         "New keys for %s. Send them their new links:",
		"new_sub":          "New subscription link for %s:",
		"deleted":          "Deleted %s.",

		"b_yes": "Yes", "b_cancel": "Cancel", "b_users": "« Users", "b_back": "« Back", "b_prev": "‹ Previous",
		"b_next": "Next ›", "b_links": "🔗 Links", "b_disable": "⏸ Disable", "b_enable": "▶️ Enable",
		"b_renew": "🔁 Renew", "b_resetusage": "↺ Reset usage", "b_rotate": "🔑 New keys", "b_delete": "🗑 Delete",
		"b_resetsub": "New sub link", "b_invite": "📲 Telegram invite", "b_refresh": "↻ Refresh",

		"no_users":    "No users yet. Add one: <code>/add NAME [GB] [DAYS] [DEVICES]</code>",
		"users_title": "<b>Users</b> (%d)",
		"page":        ", page %d of %d",
		"online_n":    " · %d online",

		"st_active": "active", "st_disabled": "disabled", "st_expired": "expired", "st_over quota": "over quota",
		"used":           "Used: %s",
		"used_of":        "%s of %s (%d%%)",
		"no_quota":       "(no quota)",
		"resets":         "Resets: %s",
		"monthly":        "monthly, next on %s",
		"weekly":         "weekly, next on %s",
		"expires":        "Expires: %s",
		"never":          "never",
		"expired_on":     "expired %s",
		"days_left":      "%s (%d days left)",
		"devices":        "Devices: %s",
		"devices_online": "%d online",
		"devices_limit":  ", limit %d",
		"last_seen":      "Last seen: %s",
		"now":            "now",
		"last7":          "Last 7 days: %s %s",
		"plan":           "Plan: %s",
		"owner":          "Reseller: %s",
		"hint_limit":     "Change limits: /limit %s quota=GB days=N devices=N",
		"disabled_tag":   " (disabled)",
		"sub_link":       "Subscription (for apps, and a page with usage):",
		"qr_sub":         "%s: subscription",
		"invite":         "Send this to %s. When they open it, the bot shows them their usage and links, and warns them before their quota or time runs out:\n\n%s",

		"unlimited":    "unlimited",
		"res_status":   "<b>Your users</b>: %d of %s\nQuota given out: %s of %s\nUsed: %s",
		"users_line":   "Users: %d, %d active, %d online now",
		"traffic":      "Traffic: %s",
		"off":          "off",
		"sub_server":   "Subscription server: %s",
		"configs":      "<b>Configs</b>",
		"follower":     "follower",
		"quiet":        "no traffic for %s: blocked?",
		"active_ago":   "used %s ago, %s in total",
		"no_traffic":   "no traffic yet",
		"no_plans":     "No plans yet. On the server: <code>xtune plans add basic --quota 50 --days 30 --devices 2</code>",
		"plans_title":  "<b>Plans</b>",
		"no_resellers": "No resellers. On the server: <code>xtune bot resellers add ID --users 20 --quota 1000</code>",

		"not_authorized": "Not authorized. Your Telegram user ID is <code>%s</code>.",
		"send_link":      "Send me your subscription link (or open the invite link you were given) to see your usage here.\n\nYour Telegram user ID: <code>%s</code>",
		"linked":         "Linked to %s. You will get a message before your quota or time runs out.",
		"unlinked":       "Unlinked. Send your subscription link again to come back.",

		"backup_caption": "Encrypted xtune backup of %s (%d users). Restore on a new server (asks for the passphrase):\n<code>xtune migrate --backup FILE --to NEWHOST</code>",

		"u_quota80":  "You have used %d%% of your %s quota.",
		"u_quota":    "Your %s quota is used up, so your configs stopped working. Ask for a renewal.",
		"u_expiring": "Your account expires on %s.",
		"u_expired":  "Your account has expired, so your configs stopped working. Ask for a renewal.",
	},
	"fa": {
		"cmd_users": "فهرست کاربران", "cmd_user": "NAME: مصرف، محدودیت‌ها و کارها",
		"cmd_add": "NAME [PLAN | GB DAYS DEVICES]: افزودن کاربر", "cmd_renew": "NAME [PLAN]: تمدید کاربر",
		"cmd_links": "NAME: لینک‌ها و کد QR", "cmd_status": "وضعیت سرور", "cmd_help": "همهٔ دستورها",

		"help": `<b>xtune</b>

/users - فهرست کاربران
/user NAME - مصرف، محدودیت‌ها و کارها
/add NAME [GB] [DAYS] [DEVICES] - افزودن کاربر (۰ = نامحدود / بدون انقضا)
/add NAME PLAN - افزودن کاربر با محدودیت‌های یک طرح
/limit NAME quota=GB days=N devices=N reset=monthly|weekly|none - تغییر محدودیت‌ها
/renew NAME [PLAN] - تمدید: انقضا جلو می‌رود و مصرف صفر می‌شود
/links NAME - لینک اشتراک، کانفیگ‌ها و کد QR
/enable NAME و /disable NAME - فعال و غیرفعال کردن
/resetusage NAME - صفر کردن مصرف
/rotate NAME - کلیدهای تازه (لینک‌های قبلی از کار می‌افتند)
/resetsub NAME - لینک اشتراک تازه
/del NAME - حذف کاربر
/plans - طرح‌ها
/status - وضعیت سرور`,
		"help_admin": `/resellers - نماینده‌ها
/backup - پشتیبان رمزگذاری‌شده، همین حالا`,
		"help_examples": "مثال: <code>/add carol 50 30 2</code>، <code>/add dave basic</code>، <code>/limit carol quota=100 reset=monthly</code>",
		"help_user": `/me - مصرف و تاریخ انقضای شما
/links - کانفیگ‌ها و کد QR شما
/unlink - جدا کردن حساب شما از این ربات`,

		"err_unknown":        "دستور /%s شناخته نشد؛ /help را ببینید",
		"err_needs_name":     "/%s نام کاربر می‌خواهد: /%s NAME",
		"err_no_user":        "کاربری به نام %q نیست",
		"err_name_long":      "این نام برای دکمه‌ها بلند است؛ این کار را روی سرور انجام دهید (xtune users)",
		"err_limit_usage":    "بگویید چه چیزی تغییر کند: /limit NAME quota=GB days=N devices=N reset=monthly|weekly|none",
		"err_res_users":      "حداکثر %s کاربر می‌توانید داشته باشید",
		"err_res_unlimited":  "کاربران شما باید سهمیه داشته باشند، مثلاً: /add NAME 20 30",
		"err_res_quota":      "این بیشتر از سهمیهٔ باقی‌ماندهٔ شماست (%s)",
		"err_no_selfservice": "بخش کاربران خاموش است (روی سرور: xtune bot self-service on)",
		"err_no_backup_pass": "هنوز رمز پشتیبان تعیین نشده؛ روی سرور: xtune bot backup on",

		"confirm_rotate":   "به %s کلید تازه داده شود؟ لینک‌های فعلی‌اش تا وارد کردن لینک‌های تازه کار نمی‌کنند.",
		"confirm_resetsub": "به %s لینک اشتراک تازه داده شود؟ لینک فعلی از کار می‌افتد.",
		"confirm_del":      "%s حذف شود؟ لینک‌هایش از کار می‌افتند.",
		"added":            "%s اضافه شد.",
		"renewed":          "تمدید شد: انقضا %s، مصرف صفر شد.",
		"done_limits":      "محدودیت‌ها تغییر کرد.",
		"done_enable":      "فعال شد.",
		"done_disable":     "غیرفعال شد.",
		"done_resetusage":  "مصرف صفر شد.",
		"new_keys":         "کلیدهای تازهٔ %s. لینک‌های تازه را برایش بفرستید:",
		"new_sub":          "لینک اشتراک تازهٔ %s:",
		"deleted":          "%s حذف شد.",

		"b_yes": "بله", "b_cancel": "انصراف", "b_users": "« کاربران", "b_back": "« بازگشت", "b_prev": "‹ قبلی",
		"b_next": "بعدی ›", "b_links": "🔗 لینک‌ها", "b_disable": "⏸ غیرفعال", "b_enable": "▶️ فعال",
		"b_renew": "🔁 تمدید", "b_resetusage": "↺ صفر کردن مصرف", "b_rotate": "🔑 کلید تازه", "b_delete": "🗑 حذف",
		"b_resetsub": "لینک اشتراک تازه", "b_invite": "📲 دعوت در تلگرام", "b_refresh": "↻ به‌روزرسانی",

		"no_users":    "هنوز کاربری نیست. افزودن: <code>/add NAME [GB] [DAYS] [DEVICES]</code>",
		"users_title": "<b>کاربران</b> (%s)",
		"page":        "، صفحهٔ %s از %s",
		"online_n":    " · %s آنلاین",

		"st_active": "فعال", "st_disabled": "غیرفعال", "st_expired": "منقضی", "st_over quota": "سهمیه تمام شده",
		"used":           "مصرف: %s",
		"used_of":        "%s از %s (%s٪)",
		"no_quota":       "(بدون سهمیه)",
		"resets":         "صفر شدن مصرف: %s",
		"monthly":        "ماهانه، بعدی در %s",
		"weekly":         "هفتگی، بعدی در %s",
		"expires":        "انقضا: %s",
		"never":          "هرگز",
		"expired_on":     "منقضی‌شده در %s",
		"days_left":      "%s (%s روز مانده)",
		"devices":        "دستگاه‌ها: %s",
		"devices_online": "%s آنلاین",
		"devices_limit":  "، حداکثر %s",
		"last_seen":      "آخرین اتصال: %s",
		"now":            "همین حالا",
		"last7":          "۷ روز گذشته: %s %s",
		"plan":           "طرح: %s",
		"owner":          "نماینده: %s",
		"hint_limit":     "تغییر محدودیت‌ها: /limit %s quota=GB days=N devices=N",
		"disabled_tag":   " (غیرفعال)",
		"sub_link":       "لینک اشتراک (برای برنامه‌ها، و صفحه‌ای با میزان مصرف):",
		"qr_sub":         "%s: اشتراک",
		"invite":         "این را برای %s بفرستید. با باز کردنش، ربات مصرف و لینک‌هایش را نشانش می‌دهد و پیش از تمام شدن سهمیه یا زمانش خبرش می‌کند:\n\n%s",

		"unlimited":    "نامحدود",
		"res_status":   "<b>کاربران شما</b>: %s از %s\nسهمیهٔ داده‌شده: %s از %s\nمصرف: %s",
		"users_line":   "کاربران: %s، %s فعال، %s آنلاین",
		"traffic":      "ترافیک: %s",
		"off":          "خاموش",
		"sub_server":   "سرور اشتراک: %s",
		"configs":      "<b>کانفیگ‌ها</b>",
		"follower":     "سرور پیرو",
		"quiet":        "%s بدون ترافیک: مسدود شده؟",
		"active_ago":   "آخرین استفاده %s پیش، روی هم %s",
		"no_traffic":   "هنوز ترافیکی نداشته",
		"no_plans":     "هنوز طرحی نیست. روی سرور: <code>xtune plans add basic --quota 50 --days 30 --devices 2</code>",
		"plans_title":  "<b>طرح‌ها</b>",
		"no_resellers": "نماینده‌ای نیست. روی سرور: <code>xtune bot resellers add ID --users 20 --quota 1000</code>",

		"not_authorized": "دسترسی ندارید. شناسهٔ کاربری تلگرام شما: <code>%s</code>",
		"send_link":      "لینک اشتراکتان را بفرستید (یا لینک دعوتی را که گرفته‌اید باز کنید) تا مصرفتان را اینجا ببینید.\n\nشناسهٔ کاربری تلگرام شما: <code>%s</code>",
		"linked":         "به %s وصل شد. پیش از تمام شدن سهمیه یا زمانتان پیام می‌گیرید.",
		"unlinked":       "جدا شد. برای برگشتن، لینک اشتراکتان را دوباره بفرستید.",

		"backup_caption": "پشتیبان رمزگذاری‌شدهٔ xtune از %s (%s کاربر). بازگردانی روی سرور تازه (رمز را می‌پرسد):\n<code>xtune migrate --backup FILE --to NEWHOST</code>",

		"u_quota80":  "%s٪ از سهمیهٔ %s شما مصرف شده است.",
		"u_quota":    "سهمیهٔ %s شما تمام شد و کانفیگ‌هایتان از کار افتاد. برای تمدید پیام دهید.",
		"u_expiring": "حساب شما در %s منقضی می‌شود.",
		"u_expired":  "حساب شما منقضی شد و کانفیگ‌هایتان از کار افتاد. برای تمدید پیام دهید.",
	},
}
