package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
)

var db *pgxpool.Pool

func main() {
	runMigrations()

	ctx := context.Background()

	var err error
	db, err = pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Fatal(err)
	}

	bot, err := tgbotapi.NewBotAPI(os.Getenv("BOT_TOKEN"))
	if err != nil {
		log.Fatal(err)
	}

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := bot.GetUpdatesChan(u)

	for update := range updates {

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

func runMigrations() {
	m, err := migrate.New("file://migrations", os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Fatal(err)
	}

	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		log.Fatal(err)
	}

	log.Println("migrations applied")
}
