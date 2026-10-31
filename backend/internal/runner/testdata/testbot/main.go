// Command testbot is a stand-in bot for runner tests. Its behaviour is
// chosen by the MODE variable:
//
//	serve      listen on PORT until SIGTERM (default)
//	crash      exit 2 right away
//	slow       listen only after 30 s
//	stubborn   ignore SIGTERM
package main

import (
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"
)

func main() {
	mode := os.Getenv("MODE")
	fmt.Println("testbot starting, mode", mode)
	fmt.Fprintln(os.Stderr, "to stderr")
	switch mode {
	case "crash":
		fmt.Println("panic: boom")
		os.Exit(2)
	case "slow":
		time.Sleep(30 * time.Second)
	case "stubborn":
		signal.Ignore(syscall.SIGTERM)
	}
	http.HandleFunc("/env", func(w http.ResponseWriter, r *http.Request) {
		env := os.Environ()
		sort.Strings(env)
		fmt.Fprint(w, strings.Join(env, "\n"))
	})
	http.HandleFunc("/whoami", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%d %d", os.Getuid(), os.Getgid())
	})
	http.HandleFunc("/read", func(w http.ResponseWriter, r *http.Request) {
		_, err := os.ReadFile(r.URL.Query().Get("path"))
		fmt.Fprint(w, err)
	})
	http.HandleFunc("/limits", func(w http.ResponseWriter, r *http.Request) {
		b, _ := os.ReadFile("/proc/self/limits")
		w.Write(b)
	})
	go func() {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGTERM)
		<-ch
		if mode != "stubborn" {
			fmt.Println("got SIGTERM")
			os.Exit(0)
		}
	}()
	if err := http.ListenAndServe("127.0.0.1:"+os.Getenv("PORT"), nil); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}
