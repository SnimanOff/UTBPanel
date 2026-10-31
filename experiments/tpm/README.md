# Эксперимент с TPM

Отдельная программа, в продукт не входит. Только Windows.

    go run .            ключ в TPM (Microsoft Platform Crypto Provider)
    go run . -no-tpm    программный ключ под DPAPI
    go run . -delete    удалить ключ

## Итог (Windows 11, 2026-10-31)

- Библиотека: свои вызовы ncrypt.dll через golang.org/x/sys/windows
  (LazyDLL), без cgo и без github.com/google/certtostore. Нужно всего девять
  функций CNG, обёртка — около 150 строк, лишних зависимостей нет.
- Ключ ECDSA P-256 создаётся в TPM, повторный запуск открывает тот же ключ
  по имени.
- Экспорт закрытого ключа (ECCPRIVATEBLOB) отклоняется с NTE_NOT_SUPPORTED
  (0x8009000a). Открытый ключ (ECCPUBLICBLOB) читается.
- TLS 1.3 с RequireAndVerifyClientCert проходит, схема подписи
  ecdsa_secp256r1_sha256. NCryptSignHash возвращает r||s — переводим в ASN.1.
- Время одной подписи в TPM: около 50 мс (для сравнения: программный ключ —
  0,1 мс). Рукопожатие с запросом — около 43 мс.
- Режим -no-tpm (DPAPI, CryptProtectData) работает так же.

## Найденные проблемы

- NCryptSetProperty("Export Policy") с флагом NCRYPT_OVERWRITE_KEY_FLAG
  отклоняется с NTE_BAD_FLAGS (0x80090009) — флаги должны быть 0. Ключи TPM
  неизвлекаемые и без этого свойства, ставим его явно на всякий случай.

## Решение

- gate держит одно постоянное соединение HTTP/2 с панелью (коммит 017), а не
  делает рукопожатие на каждый запрос: 50 мс на подпись заметны.
- TPM работает, запасной вариант с DPAPI тоже.
