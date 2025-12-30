# EthSpeed

EthSpeed — простой self-hosted speed test: один Go-бинарник поднимает HTTP-сервер для измерения download/upload скорости и отдаёт веб-интерфейс, вшитый в бинарник через `go:embed`.

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

```bash
go build -o ethspeed .
./ethspeed -mode server -host 0.0.0.0 -port 8080
```

Открыть UI:
- http://localhost:8080/

Скачать бинарник с сервера:
- http://localhost:8080/ethspeed

### Docker

```bash
docker build -t ethspeed:latest .
docker run --rm -p 8080:8080 ethspeed:latest
```

## Запуск (клиент)

Пример: 100 MB, 3 прогона, download+upload:

```bash
./ethspeed -mode client -server 127.0.0.1:8080 -size 100 -count 3 -direction both
```

Параметры:
- `-server` — `host:port`
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

```
.
├── main.go           # основной код
├── go.mod            # модуль
├── go.sum            # зависимости
├── Dockerfile        # сборка в контейнер
└── http/             # статические файлы (UI)
    └── index.html    # веб-интерфейс
```

## Разработка

Статика встраивается в бинарник через `go:embed`, поэтому итоговый бинарник содержит всё необходимое для запуска.

## Лицензия

MIT — см. файл `LICENSE`.
