package runner

import (
	"bufio"
	"io"
	"time"
)

// MaxLine is the longest line kept; the rest of a longer line is cut.
const MaxLine = 16 << 10

func (r *Runner) pipe(id int64, instance, stream string, rd io.Reader) {
	br := bufio.NewReaderSize(rd, MaxLine)
	for {
		line, isPrefix, err := br.ReadLine()
		if len(line) > 0 || (err == nil && !isPrefix) {
			text := string(line)
			if isPrefix {
				text += " …[cut]"
				// Skip the rest of the long line.
				for isPrefix && err == nil {
					_, isPrefix, err = br.ReadLine()
				}
			}
			if r.cfg.Hooks.Output != nil {
				r.cfg.Hooks.Output(Line{BotID: id, Stream: stream, Text: text, Time: time.Now().UTC(), Instance: instance})
			}
		}
		if err != nil {
			return
		}
	}
}
