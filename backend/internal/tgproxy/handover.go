package tgproxy

import (
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// Listener is a bot proxy port handed to the next core.
type Listener struct {
	BotID    int64  `json:"bot_id"`
	Instance string `json:"instance"`
	File     int    `json:"file"`
}

// Handover returns the listening sockets, so the bots' Bot API calls keep
// their port across a core restart. The caller closes the files.
func (p *Proxy) Handover() ([]Listener, []*os.File, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []Listener
	var files []*os.File
	for k, ln := range p.lns {
		tl, ok := ln.(*net.TCPListener)
		if !ok {
			continue
		}
		f, err := tl.File()
		if err != nil {
			for _, f := range files {
				f.Close()
			}
			return nil, nil, err
		}
		id, inst, _ := strings.Cut(k, "/")
		botID, _ := strconv.ParseInt(id, 10, 64)
		out = append(out, Listener{BotID: botID, Instance: inst, File: len(files)})
		files = append(files, f)
	}
	return out, files, nil
}

// Adopt serves listeners handed over by the previous core.
func (p *Proxy) Adopt(ls []Listener, files []*os.File) error {
	for _, l := range ls {
		ln, err := net.FileListener(files[l.File])
		files[l.File].Close()
		if err != nil {
			return err
		}
		p.mu.Lock()
		if p.lns == nil {
			p.lns = map[string]net.Listener{}
		}
		p.lns[key(l.BotID, l.Instance)] = ln
		p.mu.Unlock()
		srv := &http.Server{Handler: &handler{p: p, bot: l.BotID, inst: l.Instance}, ReadHeaderTimeout: 30 * time.Second}
		go srv.Serve(ln)
	}
	return nil
}
