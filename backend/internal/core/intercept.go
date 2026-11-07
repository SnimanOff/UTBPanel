package core

import "net/http"

// intercept answers some Bot API calls of a bot locally. Only the core
// manages the webhook: the bot's own setWebhook and deleteWebhook get "ok"
// without reaching Telegram (so the bot and the core never fight, and the
// bot needs no certificate). getUpdates is served from the panel's webhook
// when «Включить вебхук» is on and refused otherwise. getWebhookInfo passes
// through, except with the switch on, where the bot sees no webhook.
func (c *Core) intercept(r *http.Request, botID int64, instance, method string) ([]byte, int, bool) {
	switch method {
	case "setWebhook":
		c.Logs.Add(botID, "core", "bot called "+method+": answered ok, the webhook is managed by the panel", nowUTC())
		return []byte(`{"ok":true,"result":true,"description":"tgpanel: the webhook is managed by the panel"}`), http.StatusOK, true
	case "deleteWebhook":
		if c.pollingOn(botID, instance) {
			c.deleteWebhookPolling(r, botID, instance)
			return []byte(`{"ok":true,"result":true,"description":"Webhook is already deleted"}`), http.StatusOK, true
		}
		c.Logs.Add(botID, "core", "bot called "+method+": answered ok, the webhook is managed by the panel", nowUTC())
		return []byte(`{"ok":true,"result":true,"description":"tgpanel: the webhook is managed by the panel"}`), http.StatusOK, true
	case "getWebhookInfo":
		if body, ok := c.webhookInfoPolling(botID, instance); ok {
			return body, http.StatusOK, true
		}
	case "getUpdates":
		if c.pollingOn(botID, instance) {
			body, status := c.getUpdates(r, botID, instance)
			return body, status, true
		}
		c.Logs.Add(botID, "core", "bot called getUpdates: refused, the bot works through the webhook (turn on «Включить вебхук» in the Behaviour tab)", nowUTC())
		return refusedPolling, http.StatusConflict, true
	}
	return nil, 0, false
}
