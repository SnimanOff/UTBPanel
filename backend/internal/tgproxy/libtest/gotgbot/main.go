// go-telegram/bot: bot.New calls getMe.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/go-telegram/bot"
)

func main() {
	b, err := bot.New(os.Getenv("BOT_TOKEN"), bot.WithSkipGetMe())
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	me, err := b.GetMe(context.Background())
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	fmt.Println("username:", me.Username)
}
