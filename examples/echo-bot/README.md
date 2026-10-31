# echo-bot

Эталонный бот без зависимостей от панели: обычный webhook-бот, все
настройки — из переменных окружения. Отвечает эхом методом в теле ответа на
вебхук и считает сообщения в SQLite (modernc.org/sqlite, без cgo).

## Локально

    go build -o bot .
    PORT=8081 WEBHOOK_SECRET=s DATABASE_URL=file:bot.db ./bot

Вебхук для локального запуска ставится вручную (setWebhook на адрес,
доступный Telegram, например через туннель).

## В панели

1. Собрать статический бинарник: `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o bot .`
2. «+ Бот» → имя и токен.
3. «Деплой» → «Загрузить» → файл `bot`, затем «Старт».

Вебхук ставит ядро панели; BOT_TOKEN, PORT, WEBHOOK_SECRET и DATABASE_URL
бот получает сам. Для релизов из GitHub в репозитории есть
`.goreleaser.yaml` и `.github/workflows/release.yml`: тег `v1.2.3` собирает
`tar.gz` и `checksums.txt`.
