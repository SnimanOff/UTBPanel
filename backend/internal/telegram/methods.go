package telegram

import (
	"context"
	"encoding/json"
)

// User is a Telegram user or bot.
type User struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	Username  string `json:"username"`
}

// GetMe checks a token and returns the bot.
func (c *Client) GetMe(ctx context.Context, token string) (User, error) {
	var u User
	err := c.Call(ctx, token, "getMe", nil, &u)
	return u, err
}

// SetWebhook parameters.
type SetWebhook struct {
	URL                string    `json:"url"`
	SecretToken        string    `json:"secret_token,omitempty"`
	Certificate        InputFile `json:"certificate,omitzero"`
	MaxConnections     int       `json:"max_connections,omitempty"`
	DropPendingUpdates bool      `json:"drop_pending_updates,omitempty"`
	AllowedUpdates     []string  `json:"allowed_updates,omitempty"`
}

func (p SetWebhook) files() map[string]InputFile {
	return map[string]InputFile{"certificate": p.Certificate}
}

// SetWebhook installs the webhook.
func (c *Client) SetWebhook(ctx context.Context, token string, p SetWebhook) error {
	return c.Call(ctx, token, "setWebhook", p, nil)
}

// DeleteWebhook removes the webhook; pending updates stay with Telegram
// unless drop is set.
func (c *Client) DeleteWebhook(ctx context.Context, token string, drop bool) error {
	return c.Call(ctx, token, "deleteWebhook", map[string]any{"drop_pending_updates": drop}, nil)
}

// WebhookInfo is the answer of getWebhookInfo.
type WebhookInfo struct {
	URL                  string `json:"url"`
	HasCustomCertificate bool   `json:"has_custom_certificate"`
	PendingUpdateCount   int    `json:"pending_update_count"`
	IPAddress            string `json:"ip_address,omitempty"`
	LastErrorDate        int64  `json:"last_error_date,omitempty"`
	LastErrorMessage     string `json:"last_error_message,omitempty"`
	MaxConnections       int    `json:"max_connections,omitempty"`
}

// GetWebhookInfo reads the webhook state.
func (c *Client) GetWebhookInfo(ctx context.Context, token string) (WebhookInfo, error) {
	var w WebhookInfo
	err := c.Call(ctx, token, "getWebhookInfo", nil, &w)
	return w, err
}

// BotCommand is one entry of the command menu.
type BotCommand struct {
	Command     string `json:"command" yaml:"command"`
	Description string `json:"description" yaml:"description"`
}

// MenuButton is the chat menu button.
type MenuButton struct {
	Type   string `json:"type" yaml:"type"` // commands | web_app | default
	Text   string `json:"text,omitempty" yaml:"text,omitempty"`
	WebApp *struct {
		URL string `json:"url" yaml:"url"`
	} `json:"web_app,omitempty" yaml:"web_app,omitempty"`
}

// Profile is everything the "Profile" tab edits for one language.
type Profile struct {
	Name             string       `json:"name"`
	Description      string       `json:"description"`
	ShortDescription string       `json:"short_description"`
	Commands         []BotCommand `json:"commands"`
}

func lang(code string) map[string]any {
	m := map[string]any{}
	if code != "" {
		m["language_code"] = code
	}
	return m
}

// GetProfile reads name, descriptions and commands for a language ("" =
// default).
func (c *Client) GetProfile(ctx context.Context, token, code string) (Profile, error) {
	var p Profile
	var name struct{ Name string }
	if err := c.Call(ctx, token, "getMyName", lang(code), &name); err != nil {
		return p, err
	}
	var desc struct{ Description string }
	if err := c.Call(ctx, token, "getMyDescription", lang(code), &desc); err != nil {
		return p, err
	}
	var short struct {
		ShortDescription string `json:"short_description"`
	}
	if err := c.Call(ctx, token, "getMyShortDescription", lang(code), &short); err != nil {
		return p, err
	}
	if err := c.Call(ctx, token, "getMyCommands", lang(code), &p.Commands); err != nil {
		return p, err
	}
	p.Name, p.Description, p.ShortDescription = name.Name, desc.Description, short.ShortDescription
	return p, nil
}

// SetMyName changes the bot name for a language.
func (c *Client) SetMyName(ctx context.Context, token, code, name string) error {
	m := lang(code)
	m["name"] = name
	return c.Call(ctx, token, "setMyName", m, nil)
}

// SetMyDescription changes the description.
func (c *Client) SetMyDescription(ctx context.Context, token, code, text string) error {
	m := lang(code)
	m["description"] = text
	return c.Call(ctx, token, "setMyDescription", m, nil)
}

// SetMyShortDescription changes the short description.
func (c *Client) SetMyShortDescription(ctx context.Context, token, code, text string) error {
	m := lang(code)
	m["short_description"] = text
	return c.Call(ctx, token, "setMyShortDescription", m, nil)
}

// SetMyCommands replaces the command menu.
func (c *Client) SetMyCommands(ctx context.Context, token, code string, cmds []BotCommand) error {
	m := lang(code)
	if cmds == nil {
		cmds = []BotCommand{}
	}
	m["commands"] = cmds
	return c.Call(ctx, token, "setMyCommands", m, nil)
}

// GetChatMenuButton reads the default menu button.
func (c *Client) GetChatMenuButton(ctx context.Context, token string) (MenuButton, error) {
	var b MenuButton
	err := c.Call(ctx, token, "getChatMenuButton", nil, &b)
	return b, err
}

// SetChatMenuButton sets the default menu button.
func (c *Client) SetChatMenuButton(ctx context.Context, token string, b MenuButton) error {
	return c.Call(ctx, token, "setChatMenuButton", map[string]any{"menu_button": b}, nil)
}

// Message is the part of a sent message the panel needs.
type Message struct {
	MessageID int64 `json:"message_id"`
	Photo     []struct {
		FileID string `json:"file_id"`
	} `json:"photo,omitempty"`
}

// SendMessage parameters.
type SendMessage struct {
	ChatID      int64           `json:"chat_id"`
	Text        string          `json:"text"`
	ParseMode   string          `json:"parse_mode,omitempty"`
	ReplyMarkup json.RawMessage `json:"reply_markup,omitempty"`
}

// SendMessage sends a text message.
func (c *Client) SendMessage(ctx context.Context, token string, p SendMessage) (Message, error) {
	var m Message
	err := c.Call(ctx, token, "sendMessage", p, &m)
	return m, err
}

// SendPhoto parameters: Photo is a file_id or URL, or PhotoFile is uploaded.
type SendPhoto struct {
	ChatID      int64           `json:"chat_id"`
	Photo       string          `json:"photo,omitempty"`
	PhotoFile   InputFile       `json:"-"`
	Caption     string          `json:"caption,omitempty"`
	ParseMode   string          `json:"parse_mode,omitempty"`
	ReplyMarkup json.RawMessage `json:"reply_markup,omitempty"`
}

func (p SendPhoto) files() map[string]InputFile { return map[string]InputFile{"photo": p.PhotoFile} }

// SendPhoto sends a picture.
func (c *Client) SendPhoto(ctx context.Context, token string, p SendPhoto) (Message, error) {
	var m Message
	err := c.Call(ctx, token, "sendPhoto", p, &m)
	return m, err
}

// AnswerCallbackQuery stops the spinner on a pressed button.
func (c *Client) AnswerCallbackQuery(ctx context.Context, token, id, text string, alert bool) error {
	return c.Call(ctx, token, "answerCallbackQuery", map[string]any{"callback_query_id": id, "text": text, "show_alert": alert}, nil)
}

// AnswerPreCheckoutQuery answers a payment check.
func (c *Client) AnswerPreCheckoutQuery(ctx context.Context, token, id string, ok bool, errMsg string) error {
	m := map[string]any{"pre_checkout_query_id": id, "ok": ok}
	if !ok {
		m["error_message"] = errMsg
	}
	return c.Call(ctx, token, "answerPreCheckoutQuery", m, nil)
}

// EditMessage changes the text (or the caption of a picture) and the
// buttons of a sent message.
type EditMessage struct {
	ChatID      int64           `json:"chat_id"`
	MessageID   int64           `json:"message_id"`
	Text        string          `json:"text,omitempty"`
	Caption     string          `json:"caption,omitempty"`
	ParseMode   string          `json:"parse_mode,omitempty"`
	ReplyMarkup json.RawMessage `json:"reply_markup,omitempty"`
}

// EditMessageText edits a text message.
func (c *Client) EditMessageText(ctx context.Context, token string, p EditMessage) error {
	return c.Call(ctx, token, "editMessageText", p, nil)
}

// EditMessageCaption edits the caption of a picture.
func (c *Client) EditMessageCaption(ctx context.Context, token string, p EditMessage) error {
	return c.Call(ctx, token, "editMessageCaption", p, nil)
}

// DeleteMessage deletes a message the bot sent less than 48 hours ago.
func (c *Client) DeleteMessage(ctx context.Context, token string, chat, message int64) error {
	return c.Call(ctx, token, "deleteMessage", map[string]any{"chat_id": chat, "message_id": message}, nil)
}
