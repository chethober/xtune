package state

// Notify sends admin alerts (quota, expiry, Xray down) to Telegram and/or a webhook. The enforce
// job checks for them every minute and sends each alert once, until its condition clears.
type Notify struct {
	TelegramToken string `json:"telegramToken,omitempty"`
	TelegramChat  string `json:"telegramChat,omitempty"`
	Webhook       string `json:"webhook,omitempty"` // receives {"text","content","event","server"} as JSON
}

func (n Notify) On() bool { return n.TelegramToken != "" && n.TelegramChat != "" || n.Webhook != "" }
