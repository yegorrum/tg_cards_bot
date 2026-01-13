package main

import (
	"context"
	"fmt"
	"log"
	"microservice/service"
	"net/http"
	"os"
	"os/signal"
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
	dsn := os.Getenv("DATABASE_URL")
	service.RunMigrations(dsn)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	webhookURL := os.Getenv("WEBHOOK_URL")
	token := os.Getenv("BOT_TOKEN")

	if token == "" || webhookURL == "" {
		log.Fatal("TG_TOKEN or WEBHOOK_URL not set")
	}

	ctx := context.Background()

	var err error
	db, err = pgxpool.New(ctx, dsn)
	if err != nil {
		log.Fatal(err)
	}

	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		log.Fatal(err)
	}

	bot.Debug = false
	log.Printf("Authorized as %s", bot.Self.UserName)

	// включаем webhook
	wh, err := tgbotapi.NewWebhook(webhookURL)
	if err != nil {
		log.Fatal(err)
	}

	if _, err := bot.Request(wh); err != nil {
		log.Fatal(err)
	}

	info, _ := bot.GetWebhookInfo()
	log.Printf("Webhook set to %s", info.URL)

	updates := bot.ListenForWebhook("/webhook")

	server := &http.Server{Addr: ":" + port}

	// сигналы
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		log.Printf("HTTP server started on :%s", port)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("ListenAndServe error: %v", err)
		}
	}()

	// обработка апдейтов
	go func() {
		for update := range updates {
			handleUpdate(bot, update)
		}
	}()

	<-stop
	log.Println("Shutdown signal received")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// выключаем webhook
	if _, err := bot.Request(tgbotapi.DeleteWebhookConfig{}); err != nil {
		log.Printf("DeleteWebhook error: %v", err)
	} else {
		log.Println("Webhook disabled")
	}

	if err := server.Shutdown(ctx); err != nil {
		log.Printf("HTTP shutdown error: %v", err)
	}

	log.Println("Bot stopped gracefully")

	// u := tgbotapi.NewUpdate(0)
	// u.Timeout = 60
	// updates := bot.GetUpdatesChan(u)

	// for update := range updates {

	// 	if update.Message != nil && update.Message.IsCommand() {
	// 		switch update.Message.Command() {

	// 		case "add":
	// 			handleAdd(bot, update)

	// 		case "players":
	// 			sendPlayers(bot, update.Message.Chat.ID)
	// 		}
	// 	}

	// 	if update.CallbackQuery != nil {
	// 		data := update.CallbackQuery.Data
	// 		parts := strings.Split(data, ":")

	// 		switch parts[0] {

	// 		case "minus":
	// 			handleClickDecrease(bot, update, parts[1])
	// 		case "plus":
	// 			handleClickIncrease(bot, update, parts[1])
	// 		}
	// 	}
	// }
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

func handleUpdate(bot *tgbotapi.BotAPI, update tgbotapi.Update) {

	if update.Message != nil && update.Message.IsCommand() {
		switch update.Message.Command() {

		case "add":
			handleAdd(bot, update)

		case "players":
			sendPlayers(bot, update.Message.Chat.ID)
		}
	}

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

	log.Printf(
		"Message from %d: %s",
		update.Message.Chat.ID,
		update.Message.Text,
	)

	msg := tgbotapi.NewMessage(
		update.Message.Chat.ID,
		"Привет! Я получил твоё сообщение 👍",
	)

	bot.Send(msg)
}
