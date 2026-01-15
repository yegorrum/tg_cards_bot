package main

import (
	"context"
	"fmt"
	"log"
	"microservice/service"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
)

var db *pgxpool.Pool

func main() {
	dsn := mustEnv("DATABASE_URL")
	service.RunMigrations(dsn)

	ctx := context.Background()
	db = service.InitDB(dsn, ctx)

	token := mustEnv("BOT_TOKEN")
	mode := mustEnv("BOT_MODE")

	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		log.Fatalf("bot init error: %v", err)
	}

	log.Printf("bot authorized as %s", bot.Self.UserName)

	ctxUpd, stop := signal.NotifyContext(
		ctx,
		syscall.SIGINT,
		syscall.SIGTERM,
	)
	defer stop()

	switch mode {
	case "polling":
		bot.Debug = true
		runPolling(ctxUpd, bot)

	case "webhook":
		bot.Debug = false
		runWebhook(ctxUpd, bot)

	default:
		log.Fatalf("unknown BOT_MODE: %s", mode)
	}
}

func runPolling(ctx context.Context, bot *tgbotapi.BotAPI) {
	log.Println("starting in polling mode")

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60

	updates := bot.GetUpdatesChan(u)

	for {
		select {
		case <-ctx.Done():
			log.Println("polling shutdown")
			bot.StopReceivingUpdates()
			return

		case update := <-updates:
			handleUpdate(bot, update)
		}
	}
}

func runWebhook(ctx context.Context, bot *tgbotapi.BotAPI) {
	webhookURL := mustEnv("WEBHOOK_URL")
	portStr := mustEnv("WEBHOOK_PORT")

	port, err := strconv.Atoi(portStr)
	if err != nil {
		log.Fatalf("invalid port: %v", err)
	}

	log.Println("starting in webhook mode")

	wh, err := tgbotapi.NewWebhook(webhookURL)
	if err != nil {
		log.Fatalf("webhook error: %v", err)
	}

	if _, err := bot.Request(wh); err != nil {
		log.Fatalf("set webhook error: %v", err)
	}

	updates := bot.ListenForWebhook("/webhook")

	server := &http.Server{
		Addr:              ":" + portStr,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		log.Printf("http server listening on :%d", port)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("http server error: %v", err)
		}
	}()

	for {
		select {
		case <-ctx.Done():
			log.Println("webhook shutdown started")

			// 1️⃣ удалить webhook у Telegram
			if _, err := bot.Request(tgbotapi.DeleteWebhookConfig{}); err != nil {
				log.Printf("delete webhook error: %v", err)
			} else {
				log.Println("webhook deleted")
			}

			// 2️⃣ аккуратно остановить HTTP
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = server.Shutdown(shutdownCtx)

			return

		case update := <-updates:
			handleUpdate(bot, update)
		}
	}
}

func mustEnv(key string) string {
	val := os.Getenv(key)
	if val == "" {
		log.Fatalf("env %s is required", key)
	}
	return val
}

func handleUpdate(bot *tgbotapi.BotAPI, update tgbotapi.Update) {

	if update.CallbackQuery != nil {
		data := update.CallbackQuery.Data
		parts := strings.Split(data, ":")

		switch parts[0] {

		case "minus":
			handleClickDecrease(bot, update, parts[1])
		case "plus":
			handleClickIncrease(bot, update, parts[1])
		}
	}

	if update.Message != nil && update.Message.IsCommand() {
		switch update.Message.Command() {

		case "add":
			handleAdd(bot, update)

		case "players":
			sendPlayers(bot, update.Message.Chat.ID)
		}
	} else if update.Message != nil && !update.Message.IsCommand() {
		log.Printf(
			"Message from %d: %s",
			update.Message.Chat.ID,
			update.Message.Text,
		)

		text := fmt.Sprint("Привет! Я получил твоё сообщение:", update.Message.Text)
		msg := tgbotapi.NewMessage(
			update.Message.Chat.ID,
			text,
		)

		if _, err := bot.Send(msg); err != nil {
			log.Printf("send error: %v", err)
		}
	} else if update.Message == nil {
		return
	}

}

func handleAdd(bot *tgbotapi.BotAPI, update tgbotapi.Update) {
	chatID := update.Message.Chat.ID
	args := strings.TrimSpace(update.Message.CommandArguments())

	if args == "" {
		bot.Send(tgbotapi.NewMessage(chatID, "Используй: /add Имя"))
		return
	}

	_, err := db.Exec(
		context.Background(),
		`INSERT INTO players (chat_id, name) VALUES ($1, $2)
		 ON CONFLICT (chat_id, name) DO NOTHING`,
		chatID, args,
	)

	if err != nil {
		log.Println(err)
	}

	sendPlayers(bot, chatID)
}

func sendPlayers(bot *tgbotapi.BotAPI, chatID int64) {
	Result := buildKeyboard(chatID)

	msg := tgbotapi.NewMessage(chatID, "Игроки:")
	msg.ReplyMarkup = Result
	bot.Send(msg)
}

func buildKeyboard(chatID int64) tgbotapi.InlineKeyboardMarkup {
	rows, err := db.Query(
		context.Background(),
		`SELECT name, score FROM players
		 WHERE chat_id=$1 ORDER BY score DESC`,
		chatID,
	)

	var keyboard [][]tgbotapi.InlineKeyboardButton
	if err != nil {
		log.Println(err)
		return tgbotapi.NewInlineKeyboardMarkup(keyboard...)
	}

	defer rows.Close()

	for rows.Next() {
		var name string
		var score int
		rows.Scan(&name, &score)

		btn := tgbotapi.NewInlineKeyboardButtonData(
			fmt.Sprintf("%s (%d)", name, score),
			name,
		)
		btnPlus := tgbotapi.NewInlineKeyboardButtonData(
			"+",
			fmt.Sprintf("plus:%s", name),
		)

		btnMinus := tgbotapi.NewInlineKeyboardButtonData(
			"-",
			fmt.Sprintf("minus:%s", name),
		)
		keyboard = append(keyboard, tgbotapi.NewInlineKeyboardRow(btn, btnPlus, btnMinus))
	}

	return tgbotapi.NewInlineKeyboardMarkup(keyboard...)
}

func handleClickIncrease(bot *tgbotapi.BotAPI, update tgbotapi.Update, name string) {
	chatID := update.CallbackQuery.Message.Chat.ID

	_, err := db.Exec(
		context.Background(),
		`UPDATE players SET score = score + 1
		 WHERE chat_id=$1 AND name=$2`,
		chatID, name,
	)
	if err != nil {
		log.Println(err)
	}

	edit := tgbotapi.NewEditMessageReplyMarkup(
		chatID,
		update.CallbackQuery.Message.MessageID,
		buildKeyboard(chatID),
	)
	bot.Send(edit)

	cb := tgbotapi.NewCallback(update.CallbackQuery.ID, "+1")
	bot.Request(cb)
}

func handleClickDecrease(bot *tgbotapi.BotAPI, update tgbotapi.Update, name string) {
	chatID := update.CallbackQuery.Message.Chat.ID

	_, err := db.Exec(
		context.Background(),
		`UPDATE players SET score = score - 1
		 WHERE chat_id=$1 AND name=$2`,
		chatID, name,
	)
	if err != nil {
		log.Println(err)
	}

	edit := tgbotapi.NewEditMessageReplyMarkup(
		chatID,
		update.CallbackQuery.Message.MessageID,
		buildKeyboard(chatID),
	)
	bot.Send(edit)

	cb := tgbotapi.NewCallback(update.CallbackQuery.ID, "-1")
	bot.Request(cb)
}
