package core

import "net/http"

// intercept answers some Bot API calls of a bot locally. Only the core
// manages the webhook: the bot's own setWebhook and deleteWebhook get "ok"
// without reaching Telegram (so the bot and the core never fight, and the
// bot needs no certificate), getUpdates is refused because polling cannot
// work with a webhook. getWebhookInfo passes through as is.
func (c *Core) intercept(botID int64, method string) ([]byte, int, bool) {
	switch method {
	case "setWebhook", "deleteWebhook":
		c.Logs.Add(botID, "core", "bot called "+method+": answered ok, the webhook is managed by the panel", nowUTC())
		return []byte(`{"ok":true,"result":true,"description":"tgpanel: the webhook is managed by the panel"}`), http.StatusOK, true
	case "getUpdates":
		c.Logs.Add(botID, "core", "bot called getUpdates: refused, the bot works through the webhook", nowUTC())
		return []byte(`{"ok":false,"error_code":409,"description":"Conflict: tgpanel: бот работает через вебхук, polling недоступен"}`), http.StatusConflict, true
	}
	return nil, 0, false
}
