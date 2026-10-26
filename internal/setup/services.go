package setup

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/galimru/gosha/internal/config"
	"github.com/galimru/gosha/internal/linear"
	"github.com/galimru/gosha/internal/notify"
	"github.com/galimru/gosha/internal/notion"
)

func (s *Setup) linear(ctx context.Context) error {
	s.say("In Linear, create an OAuth application (Settings → API → OAuth applications)")
	s.say("with Client credentials enabled, and copy its client id and secret.")
	var opts []linear.Option
	if s.LinearAPI != "" {
		opts = append(opts, linear.WithURLs(s.LinearAPI, s.LinearToken), linear.WithBackoff(time.Millisecond))
	}
	for {
		id, err := s.secret("client id", config.KeyLinearClientID)
		if err != nil {
			return err
		}
		secret, err := s.secret("client secret", config.KeyLinearClientSecret)
		if err != nil {
			return err
		}
		v, err := linear.New(id, secret, opts...).Viewer(ctx)
		if err != nil {
			s.fail(err)
			// Enter now keeps what was just typed.
			s.secrets[config.KeyLinearClientID], s.secrets[config.KeyLinearClientSecret] = id, secret
			continue
		}
		if err := s.save(config.KeyLinearClientID, id, config.KeyLinearClientSecret, secret); err != nil {
			return err
		}
		s.ok("acts as %q in %s", v.Name, v.Organization.Name)
		return nil
	}
}

func (s *Setup) notion(ctx context.Context) error {
	s.say("In Notion, create an internal integration at https://www.notion.so/profile/integrations")
	s.say("with only Read content, and copy its API token.")
	var opts []notion.Option
	if s.NotionAPI != "" {
		opts = append(opts, notion.WithURL(s.NotionAPI), notion.WithBackoff(time.Millisecond))
	}
	for {
		tok, err := s.secret("API token", config.KeyNotionToken)
		if err != nil {
			return err
		}
		bot, err := notion.New(tok, opts...).Me(ctx)
		if err != nil {
			s.fail(err)
			s.secrets[config.KeyNotionToken] = tok
			continue
		}
		if err := s.save(config.KeyNotionToken, tok); err != nil {
			return err
		}
		s.ok("integration %q in %s", bot.Name, bot.Bot.WorkspaceName)
		return nil
	}
}

// testMessage is sent to the channel to show it works.
const testMessage = "Gosha is set up to post here."

func (s *Setup) telegram(ctx context.Context) error {
	s.say("Create a bot with @BotFather and paste its token.")
	var bot *notify.Bot
	var me *notify.User
	for {
		tok, err := s.secret("bot token", config.KeyTelegramToken)
		if err != nil {
			return err
		}
		bot = notify.NewBot(tok, s.TelegramAPI)
		if me, err = bot.GetMe(ctx); err != nil {
			s.fail(err)
			s.secrets[config.KeyTelegramToken] = tok
			continue
		}
		if err := s.save(config.KeyTelegramToken, tok); err != nil {
			return err
		}
		s.ok("@%s", me.Username)
		break
	}

	// A channel that's already set is kept if the bot can still post there.
	cur, err := config.GlobalValue(s.Home.Config(), "telegram.chat_id")
	if err != nil {
		return err
	}
	if id, _ := strconv.ParseInt(cur, 10, 64); id != 0 {
		chat, err := bot.GetChat(ctx, id)
		if err != nil {
			s.fail(fmt.Errorf("channel %d: %w", id, err))
		} else if keep, err := s.P.Confirm(fmt.Sprintf("  keep the channel %q?", chat.Title), true); err != nil {
			return err
		} else if keep {
			if err := bot.SendMessage(ctx, id, testMessage); err != nil {
				s.fail(err)
			} else {
				s.ok("%q (%d), test message sent", chat.Title, id)
				return nil
			}
		}
	}

	chat, err := s.findChannel(ctx, bot, me)
	if err != nil {
		return err
	}
	if err := s.setGlobal("telegram.chat_id", strconv.FormatInt(chat.ID, 10)); err != nil {
		return err
	}
	s.ok("%q (%d), test message sent", chat.Title, chat.ID)
	return nil
}

// findChannel waits for the bot to be made an administrator of a channel,
// confirms it with the operator, and sends a test message there. The chat
// id is never typed.
func (s *Setup) findChannel(ctx context.Context, bot *notify.Bot, me *notify.User) (*notify.Chat, error) {
	wait := s.Wait
	if wait == 0 {
		wait = 30 * time.Second
	}
	// Skip updates from before now: offset -1 returns only the latest,
	// and asking past it drops the rest.
	us, err := bot.Updates(ctx, -1, 0)
	if err != nil {
		return nil, err
	}
	var offset int64
	if len(us) > 0 {
		offset = us[len(us)-1].UpdateID + 1
	}
	s.say("Add @%s to your channel as an administrator that can post messages.", me.Username)
	s.say("Waiting for it… (Ctrl-C to stop)")
	for {
		us, err := bot.Updates(ctx, offset, wait)
		if errors.Is(err, notify.ErrWebhook) || ctx.Err() != nil {
			return nil, err
		}
		if err != nil {
			s.fail(err)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(min(wait, 5*time.Second)):
			}
			continue
		}
		for _, u := range us {
			offset = u.UpdateID + 1
			chat, ok := u.AdminChannel()
			if !ok {
				continue
			}
			use, err := s.P.Confirm(fmt.Sprintf("  use the channel %q?", chat.Title), true)
			if err != nil {
				return nil, err
			}
			if !use {
				s.say("Waiting for another channel…")
				continue
			}
			if err := bot.SendMessage(ctx, chat.ID, testMessage); err != nil {
				s.fail(fmt.Errorf("%w: let it post messages, then make it an administrator again", err))
				continue
			}
			return &chat, nil
		}
	}
}
