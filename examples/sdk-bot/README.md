# sdk-bot

Эталонный webhook-бот со всеми модулями SDK первой версии. `/buy` создаёт
заказ, если включён `new_checkout`; повторная доставка того же `update_id`
не создаёт второй заказ. Ответ Telegram возвращается в теле вебхука.

- `sdk/db`: SQLite, миграции и уникальный `update_id`.
- `kit.Ready()`: готовность после миграций, регистрации модулей и открытия порта.
- `kit.Counter("orders_created")`, `kit.Value("orders_total")`: свои метрики.
- `kit.Task("daily_digest", "0 9 * * *", ...)`: ежедневная сводка в логе
  в 09:00 UTC (12:00 по Москве).
- `kit.Flag("new_checkout", false, ...)`: управление заказами без рестарта.

## Локально без панели

Из каталога `examples/sdk-bot`:

```sh
go build -o bot .
PORT=8082 WEBHOOK_SECRET=local-secret DATABASE_URL=file:bot.db \
  TGPANEL_FLAG_NEW_CHECKOUT=true ./bot
```

PowerShell:

```powershell
$env:PORT = '8082'
$env:WEBHOOK_SECRET = 'local-secret'
$env:DATABASE_URL = 'file:bot.db'
$env:TGPANEL_FLAG_NEW_CHECKOUT = 'true'
go run .
```

`TGPANEL_SDK` должен быть не задан. В автономном режиме база открывается
как файл, расписание исполняется встроенным cron, метрики ничего не отправляют,
а значение переключателя берётся из env. Вебхук для реального бота ставится
вручную на публичный адрес, который проксирует запросы к `127.0.0.1:8082`.

Для проверки без Telegram:

```sh
curl -H 'X-Telegram-Bot-Api-Secret-Token: local-secret' \
  -H 'Content-Type: application/json' \
  -d '{"update_id":1,"message":{"chat":{"id":7},"text":"/buy"}}' \
  http://127.0.0.1:8082/
```

## В панели

1. Собрать `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o bot .`.
2. Создать бота в панели, загрузить бинарник, запустить.
3. Во вкладке «Настройки» включить `new_checkout` и при желании запустить
   `daily_digest` вручную в блоке «Задачи».
4. Выбрать `orders_created` и `orders_total` на странице метрик. Имена и
   первые значения появляются после отправки пачки SDK, примерно через 10 секунд.

Ядро передаёт `PORT`, `WEBHOOK_SECRET`, `DATABASE_URL`, `TGPANEL_SDK` и ставит
вебхук. SDK ходит к базе через ядро, а код бота остаётся тем же. Канарейка
обрабатывает свою долю апдейтов, но не запускает ежедневную задачу.

Миграции следует делать совместимыми с предыдущей версией: добавлять таблицы
и поля. `replace` в `go.mod` использует SDK из этого checkout; при переносе
бота в отдельный репозиторий нужно подключить опубликованную версию SDK.
