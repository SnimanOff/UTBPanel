package gate

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
)

// local serves the gate's own pages under /__gate/.
func (g *Gate) local(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/__gate/enroll":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, enrollPage, html.EscapeString(g.cfg.Block.KeyName))
	case "/__gate/enroll.json":
		st := g.EnrollState()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(st)
	default:
		http.NotFound(w, r)
	}
}

const enrollPage = `<!doctype html><meta charset="utf-8"><title>TG Panel · новое устройство</title>
<style>
body{background:#0c0a0f;color:#e9e4ee;font:16px "IBM Plex Sans",sans-serif;margin:0;padding:48px}
.box{border:1px solid #2b2236;background:#15111b;padding:32px;max-width:560px}
.code{font:28px "JetBrains Mono",monospace;color:#e9e4ee;border:1px solid #6b2fa3;padding:16px;margin:24px 0;letter-spacing:1px}
.muted{color:#8b8195}.err{color:#c8102e}
</style>
<div class="box">
<div class="muted">Новое устройство · %s</div>
<div id="code" class="code">…</div>
<p id="text">Открой панель на уже подключённом устройстве: там появится «Новое устройство» с этим кодом. Сверь код и подтверди удержанием кнопки.</p>
</div>
<script>
async function poll(){
  try{
    const s = await (await fetch('/__gate/enroll.json')).json()
    if(s.code) document.getElementById('code').textContent = s.code
    const t = document.getElementById('text')
    if(s.state==='approved'){ location.href='/'; return }
    if(s.state==='rejected'){ t.textContent='Регистрация отклонена в панели.'; t.className='err'; return }
    if(s.state==='expired'){ t.textContent='Срок регистрации истёк (24 ч). Выпусти новый ключ в панели.'; t.className='err'; return }
    if(s.state==='error'){ t.textContent='Ошибка: '+s.error; t.className='err' }
  }catch(e){}
  setTimeout(poll, 2000)
}
poll()
</script>`
