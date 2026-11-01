// go-telegram-bot-api v5: NewBotAPI calls getMe.
package main

import (
	"fmt"
	"os"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func main() {
	b, err := tgbotapi.NewBotAPI(os.Getenv("BOT_TOKEN"))
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	fmt.Println("username:", b.Self.UserName)
}
