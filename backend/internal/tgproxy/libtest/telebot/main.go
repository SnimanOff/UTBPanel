// telebot v4: NewBot calls getMe.
package main

import (
	"fmt"
	"os"

	tele "gopkg.in/telebot.v4"
)

func main() {
	b, err := tele.NewBot(tele.Settings{Token: os.Getenv("BOT_TOKEN")})
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	fmt.Println("username:", b.Me.Username)
}
