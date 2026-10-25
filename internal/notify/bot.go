package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ErrWebhook means the bot has a webhook set, so getUpdates can't be used.
var ErrWebhook = errors.New("the bot has a webhook set, so its updates can't be read: remove it with deleteWebhook, or use another bot")

// Bot makes synchronous Bot API calls, for gosha setup. The activity feed
// uses Telegram instead.
type Bot struct {
	token  string
	apiURL string
	http   *http.Client
}

// NewBot returns a client for the bot token. An empty apiURL means
// Telegram's.
func NewBot(token, apiURL string) *Bot {
	if apiURL == "" {
		apiURL = "https://api.telegram.org"
	}
	// No client timeout: long polls set their own through the context.
	return &Bot{token: token, apiURL: strings.TrimRight(apiURL, "/"), http: &http.Client{}}
}

// User is a Telegram user or bot.
type User struct {
	ID        int64  `json:"id"`
	Username  string `json:"username"`
	FirstName string `json:"first_name"`
}

// Chat is a Telegram chat.
type Chat struct {
	ID    int64  `json:"id"`
	Type  string `json:"type"`
	Title string `json:"title"`
}

// Update is one getUpdates entry; only the fields setup reads.
type Update struct {
	UpdateID     int64 `json:"update_id"`
	MyChatMember *struct {
		Chat          Chat `json:"chat"`
		NewChatMember struct {
			Status string `json:"status"`
		} `json:"new_chat_member"`
	} `json:"my_chat_member"`
	ChannelPost *struct {
		Chat Chat `json:"chat"`
	} `json:"channel_post"`
}

// AdminChannel returns the channel the update shows the bot is an
// administrator of: it was made one, or it saw a post there (bots only
// see channel posts as admins).
func (u Update) AdminChannel() (Chat, bool) {
	switch {
	case u.MyChatMember != nil:
		c := u.MyChatMember.Chat
		return c, c.Type == "channel" && u.MyChatMember.NewChatMember.Status == "administrator"
	case u.ChannelPost != nil:
		return u.ChannelPost.Chat, u.ChannelPost.Chat.Type == "channel"
	}
	return Chat{}, false
}

// GetMe returns the bot itself, which also checks the token.
func (b *Bot) GetMe(ctx context.Context) (*User, error) {
	var u User
	return &u, b.call(ctx, "getMe", nil, &u)
}

// GetChat returns a chat the bot can see.
func (b *Bot) GetChat(ctx context.Context, id int64) (*Chat, error) {
	var c Chat
	return &c, b.call(ctx, "getChat", map[string]any{"chat_id": id}, &c)
}

// SendMessage sends plain text to a chat.
func (b *Bot) SendMessage(ctx context.Context, chatID int64, text string) error {
	return b.call(ctx, "sendMessage", map[string]any{"chat_id": chatID, "text": text}, nil)
}

// Updates long-polls for chat member and channel post updates from offset
// on, waiting up to wait for one to arrive.
func (b *Bot) Updates(ctx context.Context, offset int64, wait time.Duration) ([]Update, error) {
	ctx, cancel := context.WithTimeout(ctx, wait+20*time.Second)
	defer cancel()
	var us []Update
	err := b.call(ctx, "getUpdates", map[string]any{
		"offset":          offset,
		"timeout":         int(wait.Seconds()),
		"allowed_updates": []string{"my_chat_member", "channel_post"},
	}, &us)
	return us, err
}

func (b *Bot) call(ctx context.Context, method string, args map[string]any, out any) error {
	body, _ := json.Marshal(args)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.apiURL+"/bot"+b.token+"/"+method, bytes.NewReader(body))
	if err != nil {
		return b.scrub(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.http.Do(req)
	if err != nil {
		return b.scrub(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var r struct {
		OK          bool            `json:"ok"`
		Description string          `json:"description"`
		Result      json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return fmt.Errorf("telegram %s: %s", method, resp.Status)
	}
	switch {
	case resp.StatusCode == http.StatusConflict && method == "getUpdates":
		return ErrWebhook
	case !r.OK:
		return fmt.Errorf("telegram %s: %s", method, r.Description)
	case out != nil:
		return json.Unmarshal(r.Result, out)
	}
	return nil
}

// scrub keeps the token, which is in the URL, out of errors.
func (b *Bot) scrub(err error) error {
	return fmt.Errorf("telegram: %s", strings.ReplaceAll(err.Error(), b.token, "***"))
}
