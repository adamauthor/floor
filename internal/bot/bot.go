// Package bot — транспорт Telegram: long polling, кнопки, отправка (docs/SPEC.md, раздел 8).
package bot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	tg "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"floor/internal/core"
)

// Handler — входящие события от владельца.
type Handler interface {
	OnCallback(ctx context.Context, data string, msgID int) string
	OnText(ctx context.Context, text string)
	OnNonText(ctx context.Context)
	OnStart(ctx context.Context)
	OnSettings(ctx context.Context)
	OnWebAppData(ctx context.Context, data string)
}

// Bot — клиент Telegram для одного владельца.
type Bot struct {
	api     *tg.Bot
	owner   int64
	token   string
	log     *slog.Logger
	handler Handler
}

// New создаёт клиента. Сеть не трогает: без неё процесс должен стартовать и ждать.
func New(token string, owner int64, serverURL string, log *slog.Logger) (*Bot, error) {
	b := &Bot{owner: owner, token: token, log: log}
	opts := []tg.Option{
		tg.WithSkipGetMe(),
		tg.WithDefaultHandler(b.onUpdate),
		tg.WithErrorsHandler(func(err error) {
			b.log.Warn("telegram_error", "error", b.scrub(err))
		}),
	}
	if serverURL != "" {
		opts = append(opts, tg.WithServerURL(serverURL))
	}
	api, err := tg.New(token, opts...)
	if err != nil {
		return nil, fmt.Errorf("telegram: %w", b.scrub(err))
	}
	b.api = api
	return b, nil
}

// Start крутит long polling до отмены ctx.
func (b *Bot) Start(ctx context.Context, h Handler) {
	b.handler = h
	b.api.Start(ctx)
}

// scrub убирает токен из ошибки: сетевые ошибки содержат полный URL запроса.
func (b *Bot) scrub(err error) error {
	if err == nil {
		return nil
	}
	return errors.New(strings.ReplaceAll(err.Error(), b.token, "***"))
}

func (b *Bot) onUpdate(ctx context.Context, _ *tg.Bot, u *models.Update) {
	switch {
	case u.CallbackQuery != nil:
		b.onCallback(ctx, u.CallbackQuery)
	case u.Message != nil:
		b.onMessage(ctx, u.Message)
	}
}

func (b *Bot) onCallback(ctx context.Context, q *models.CallbackQuery) {
	if q.From.ID != b.owner {
		b.log.Debug("foreign_update_ignored", "kind", "callback")
		return
	}
	msgID := 0
	if q.Message.Message != nil {
		msgID = q.Message.Message.ID
	}
	answer := b.handler.OnCallback(ctx, q.Data, msgID)
	if _, err := b.api.AnswerCallbackQuery(ctx, &tg.AnswerCallbackQueryParams{
		CallbackQueryID: q.ID, Text: answer,
	}); err != nil {
		b.log.Warn("callback_answer_failed", "error", b.scrub(err))
	}
}

func (b *Bot) onMessage(ctx context.Context, m *models.Message) {
	if m.From == nil || m.From.ID != b.owner {
		b.log.Debug("foreign_update_ignored", "kind", "message")
		return
	}
	if m.PinnedMessage != nil {
		return // служебное сообщение о закреплении
	}
	if m.WebAppData != nil {
		b.handler.OnWebAppData(ctx, m.WebAppData.Data)
		return
	}
	switch text := strings.TrimSpace(m.Text); {
	case text == "/start":
		b.handler.OnStart(ctx)
	case text == "/settings":
		b.handler.OnSettings(ctx)
	case text == "":
		b.handler.OnNonText(ctx)
	default:
		b.handler.OnText(ctx, text)
	}
}

// Send отправляет сообщение владельцу и возвращает его id.
func (b *Bot) Send(ctx context.Context, text string, buttons []core.Button) (int, error) {
	params := &tg.SendMessageParams{ChatID: b.owner, Text: text, ParseMode: models.ParseModeHTML}
	if len(buttons) > 0 {
		row := make([]models.InlineKeyboardButton, 0, len(buttons))
		for _, btn := range buttons {
			row = append(row, models.InlineKeyboardButton{Text: btn.Text, CallbackData: btn.Data})
		}
		params.ReplyMarkup = models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{row}}
	}
	msg, err := b.api.SendMessage(ctx, params)
	if err != nil {
		return 0, b.scrub(err)
	}
	return msg.ID, nil
}

// SetMenuApp ставит владельцу кнопку меню слева от поля ввода, открывающую страницу.
func (b *Bot) SetMenuApp(ctx context.Context, label, url string) error {
	_, err := b.api.SetChatMenuButton(ctx, &tg.SetChatMenuButtonParams{
		ChatID: b.owner,
		MenuButton: models.MenuButtonWebApp{
			Type: models.MenuButtonTypeWebApp, Text: label, WebApp: models.WebAppInfo{URL: url},
		},
	})
	return b.scrub(err)
}

// RemoveKeyboard отправляет сообщение и убирает кнопки под полем ввода.
func (b *Bot) RemoveKeyboard(ctx context.Context, text string) error {
	_, err := b.api.SendMessage(ctx, &tg.SendMessageParams{
		ChatID: b.owner, Text: text, ParseMode: models.ParseModeHTML,
		ReplyMarkup: models.ReplyKeyboardRemove{RemoveKeyboard: true},
	})
	return b.scrub(err)
}

// Setup оформляет бота: команды и тексты профиля. Кнопку меню владельцу ставит
// SetMenuApp; здесь — значение по умолчанию для остальных. Повтор безопасен.
func (b *Bot) Setup(ctx context.Context) error {
	_, err1 := b.api.SetMyCommands(ctx, &tg.SetMyCommandsParams{Commands: []models.BotCommand{
		{Command: "settings", Description: "Привычки, время и шаги"},
	}})
	_, err2 := b.api.SetMyShortDescription(ctx, &tg.SetMyShortDescriptionParams{ShortDescription: ShortDescription})
	_, err3 := b.api.SetMyDescription(ctx, &tg.SetMyDescriptionParams{Description: Description})
	_, err4 := b.api.SetChatMenuButton(ctx, &tg.SetChatMenuButtonParams{
		MenuButton: models.MenuButtonCommands{Type: models.MenuButtonTypeCommands},
	})
	return b.scrub(errors.Join(err1, err2, err3, err4))
}

// Тексты профиля бота.
const (
	ShortDescription = "Минимальный уровень дня, который держится даже в плохой день. Без стриков и стыда."
	Description      = "Floor держит пол дня — минимум, который выполним даже в плохой день.\n\n" +
		"☀️ Утром: подъём, сон, шаги и один шаг на сегодня.\n" +
		"🌙 Вечером: один вопрос и одна кнопка.\n" +
		"📅 В воскресенье: итог недели.\n\n" +
		"Привычки, время и шаги — кнопка «⚙️ Настройки» слева от поля ввода."
)

// Edit заменяет текст сообщения и убирает кнопки.
func (b *Bot) Edit(ctx context.Context, msgID int, text string) error {
	_, err := b.api.EditMessageText(ctx, &tg.EditMessageTextParams{
		ChatID: b.owner, MessageID: msgID, Text: text, ParseMode: models.ParseModeHTML,
	})
	if err == nil {
		return nil
	}
	switch msg := err.Error(); {
	case strings.Contains(msg, "message is not modified"):
		return nil
	case strings.Contains(msg, "message to edit not found"), strings.Contains(msg, "MESSAGE_ID_INVALID"):
		return core.ErrMessageGone
	}
	return b.scrub(err)
}

// Pin закрепляет сообщение без уведомления.
func (b *Bot) Pin(ctx context.Context, msgID int) error {
	_, err := b.api.PinChatMessage(ctx, &tg.PinChatMessageParams{
		ChatID: b.owner, MessageID: msgID, DisableNotification: true,
	})
	return b.scrub(err)
}

// Typing показывает индикатор набора.
func (b *Bot) Typing(ctx context.Context) error {
	_, err := b.api.SendChatAction(ctx, &tg.SendChatActionParams{ChatID: b.owner, Action: models.ChatActionTyping})
	return b.scrub(err)
}
