// Command badbot plays versions of a bot for the deploy e2e tests. The
// behaviour is baked in at build time: go build -ldflags "-X main.mode=…".
//
//	echo2     answers like the echo bot, prefixed "v2:" (canary checks)
//	crash     becomes ready, then exits after a second
//	notready  never listens
//	errors    answers 500 to every update
//	schema    adds a table to the database, then answers 500
package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var mode = "echo2"

func main() {
	port := os.Getenv("PORT")
	fmt.Println("badbot", mode, "canary="+os.Getenv("TGPANEL_CANARY"))
	switch mode {
	case "notready":
		time.Sleep(time.Hour)
	case "crash":
		go func() {
			time.Sleep(time.Second)
			fmt.Println("panic: badbot crash on purpose")
			os.Exit(2)
		}()
	case "schema":
		dsn := strings.TrimPrefix(os.Getenv("DATABASE_URL"), "file:")
		db, err := sql.Open("sqlite", "file:"+dsn+"?_pragma=busy_timeout(5000)")
		if err == nil {
			db.Exec(`CREATE TABLE IF NOT EXISTS v2_only (id INTEGER PRIMARY KEY)`)
			db.Close()
		}
	}
	http.HandleFunc("POST /", func(w http.ResponseWriter, r *http.Request) {
		if mode == "errors" || mode == "schema" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		var u struct {
			Message *struct {
				Chat struct {
					ID int64 `json:"id"`
				} `json:"chat"`
				Text string `json:"text"`
			} `json:"message"`
		}
		json.NewDecoder(r.Body).Decode(&u)
		if u.Message == nil {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"method": "sendMessage", "chat_id": u.Message.Chat.ID, "text": "v2:" + u.Message.Text})
	})
	http.ListenAndServe("127.0.0.1:"+port, nil)
}
