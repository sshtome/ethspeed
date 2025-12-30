# EthSpeed

EthSpeed — простой self-hosted speed test: один Go-бинарник поднимает HTTP-сервер для измерения download/upload скорости и отдаёт веб-интерфейс, вшитый в бинарник через `go:embed`.

В таком виде проект позволяет измерять скорость **до развернутого сервера**: для замера нужно либо подключиться тем же бинарником в режиме клиента и протестировать свой сервер, либо использовать публичный сервер по умолчанию (например `speed.cloudflare.com`), если свой сервер не поднят.

## Возможности

- Web UI: запуск теста, отображение Download/Upload, кнопка Start/Stop.
- HTTP эндпоинты для тестов:
  - `GET /__down?bytes=N` — отдаёт поток данных заданного размера.
  - `POST /__up?bytes=N` — принимает данные заданного размера.
- Скачивание запущенного бинарника:
  - `GET /ethspeed` — отдаёт текущий исполняемый файл (удобно для развёртывания).
- Статистика и healthcheck:
  - `GET /__stats`
  - `GET /health`
- Режимы работы:
  - `-mode server` — сервер
  - `-mode client` — консольный клиент для тестов

## Запуск (сервер)

### Локально

go build -o ethspeed .
./ethspeed -mode server -host 0.0.0.0 -port 8080

Открыть UI:
- http://localhost:8080/

Скачать бинарник с сервера:
- http://localhost:8080/ethspeed

### Docker

docker build -t ethspeed:latest .
docker run --rm -p 8080:8080 ethspeed:latest

## Запуск (клиент)

### Тест до своего сервера

Пример: 100 MB, 3 прогона, download+upload:

./ethspeed -mode client -server 127.0.0.1:8080 -size 100 -count 3 -direction both

### Тест до публичного сервера (если свой не поднят)

По умолчанию в коде сервер задан как `speed.cloudflare.com`, то есть можно не указывать `-server`:

./ethspeed -mode client -size 100 -count 3 -direction both

Параметры:
- `-server` — `host:port` (если не задан, используется значение по умолчанию)
- `-size` — размер в MB
- `-count` — количество прогонов
- `-direction` — `down`, `up`, или `both`

## Эндпоинты

- `GET /` — Web UI
- `GET /__down?bytes=N` — download test
- `POST /__up?bytes=N` — upload test
- `GET /__stats` — статистика сервера
- `GET /health` — healthcheck
- `GET /ethspeed` — скачать запущенный бинарник

## Структура проекта

.
├── main.go           # основной код
├── go.mod            # модуль
├── go.sum            # зависимости
├── Dockerfile        # сборка в контейнер
└── http/             # статические файлы (UI)
    └── index.html    # веб-интерфейс


## Разработка

Статика встраивается в бинарник через `go:embed`, поэтому итоговый бинарник содержит всё необходимое для запуска.

## Лицензия

MIT — см. файл `LICENSE`.
