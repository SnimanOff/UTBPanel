# UTBPanel

Лёгкая веб-панель для своих Go-ботов в Telegram.

## Бот и прокси Bot API

Ядро передаёт боту `HTTPS_PROXY`, `HTTP_PROXY` и `SSL_CERT_FILE`: стандартный
`http.Client` Go сам идёт через прокси панели, и вызовы Bot API попадают в
метрики без правок кода. Проверено (`go test -tags libs ./internal/tgproxy`):

| библиотека | через прокси |
|---|---|
| github.com/go-telegram/bot v1.27 | да, без настроек |
| gopkg.in/telebot.v4 | да, без настроек |
| github.com/go-telegram-bot-api/telegram-bot-api/v5 | да, без настроек |

Если библиотека создаёт свой `http.Transport` без `Proxy: http.ProxyFromEnvironment`,
вызовы пойдут мимо панели: бот работает, но метрик Bot API у него не будет, и
панель предупредит «бот ходит мимо прокси».
