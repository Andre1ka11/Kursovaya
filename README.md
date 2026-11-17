# Программный агент мониторинга учебных занятий

Здесь фиксируются работающие возможности, используемый стек и команды для их проверки.
Все команды ниже выполняются в PowerShell из корня проекта — папки с `go.mod`.

[План реализации](ПЛАН_РАЗРАБОТКИ.md) · [Инструкции по документам](docs/DOCS.md)

## Что готово и что добавилось

| Дата | Изменение | Проверка |
|---|---|---|
| 16.11.2026 | Этап 1: настройки, логи, фоновая работа, корректная остановка, сборка и тесты | Сценарии 1.1–1.7 ниже |
| 17.11.2026 | Упорядочены документы; README преобразован в руководство проверки | Структура проекта в конце файла |

Сейчас работает каркас приложения. Бот, БД приложения, чтение чата, календарь и модель ещё не подключены.
Для запуска этапа 1 PostgreSQL и Ollama не нужны.

Правило ведения этого README: после каждого этапа добавлять строку в историю, отдельный раздел
проверок и обновлять фактический стек. Существующие проверки сохранять; менять их только при
изменении поведения. Для каждой проверки указывать команду и ожидаемый результат.
Все Git-команды пользователь выполняет самостоятельно. Ассистент даёт команды и пояснения,
но не запускает Git, включая команды просмотра состояния, без нового указания пользователя.

## Стек технологий

### Уже используется

| Средство | Версия | Для чего | Где |
|---|---|---|---|
| Go | 1.27.1 | Основное приложение и сборка | `go.mod`, `cmd/agent/` |
| `github.com/joho/godotenv` | 1.5.1 | Чтение локального `.env` | `internal/config/` |
| `go.yaml.in/yaml/v3` | 3.0.6 | YAML и проверка неизвестных полей | `internal/config/` |
| `log/slog` | Стандартная библиотека Go | Логи: уровни debug/info/warn/error, форматы text/JSON | `cmd/agent/`, `internal/app/` |
| `context`, `os/signal`, `sync`, `time` | Стандартная библиотека Go | Сигналы остановки, горутина heartbeat, ожидание завершения | `internal/app/`, `cmd/agent/` |
| `time/tzdata` | Стандартная библиотека Go | Часовые пояса в самостоятельной Windows-сборке | `internal/config/` |
| `testing` и `go test` | Стандартные средства Go | Автоматические проверки и детектор гонок | `internal/*/*_test.go` |
| Git | Проверено с 2.51.0 | Контроль версий; `.env` и результаты сборки игнорируются | `.gitignore` |

Внешние зависимости и контрольные суммы зафиксированы в `go.mod` и `go.sum`.

### Запланировано

| Компонент | Средство | Этап |
|---|---|---|
| Личный Telegram-бот | `go-telegram/bot`, long polling | 2 |
| Хранилище | PostgreSQL и `pgx/pgxpool` | 3 |
| Календарь | `net/http`, iCalendar, `go-ical`; обработка повторов проверяется отдельно | 6 |
| Учебный чат | `gotd/td`, MTProto | 7 |
| Извлечение по правилам | `regexp`, `time` | 8 |
| Локальная модель | Ollama, Qwen, `net/http`, `encoding/json` | 9 |
| Отчёт эксперимента | `encoding/csv` | 11 |

PostgreSQL 18.6 установлен на этом компьютере, но приложение пока к нему не подключается.

## Этап 1 — каркас приложения

Добавлено: YAML-настройки, локальный `.env`, проверка значений, логирование, служебный heartbeat
и завершение по Ctrl+C с ожиданием фоновой горутины.

### 1.1 Проверить окружение и зависимости

```powershell
go version
go mod download
go mod verify
```

Ожидается:

- `go version` показывает установленный Go; проект использует 1.27.1.
- `go mod download` завершается без ошибок; при наличии пакетов в кэше вывода может не быть.
- `go mod verify` выводит `all modules verified`.

### 1.2 Проверить настройки без запуска фоновой работы

```powershell
go run ./cmd/agent -check-config
$LASTEXITCODE
```

При настройках из репозитория ожидается строка с `configuration valid`, `app=study-agent`
и `timezone=Europe/Moscow`, затем код `0`. Команда завершается сама.
Дата, время и порядок полей могут отличаться от примеров.

### 1.3 Собрать и запустить приложение

```powershell
go build -o bin/agent.exe ./cmd/agent
.\bin\agent.exe
```

Сборка проходит без ошибок; появляется `bin/agent.exe`. После запуска видна строка:

```text
level=INFO msg="agent started" app=study-agent timezone=Europe/Moscow
```

Программа продолжает работать. При уровне `info` отсутствие новых строк нормально.
Нажми **Ctrl+C**. Ожидается такая последовательность:

```text
level=INFO msg="shutdown requested" app=study-agent
level=INFO msg="agent stopped" app=study-agent
```

Когда вернётся приглашение терминала, выполни `$LASTEXITCODE`: ожидается `0`.
Для повседневного запуска без отдельной сборки можно использовать `go run ./cmd/agent`.

### 1.4 Увидеть фоновую работу

После сборки из пункта 1.3 выполни весь блок. Переменные меняются только в текущем процессе
PowerShell; после остановки исходные значения восстанавливаются.

```powershell
$previousLevel = $env:AGENT_LOG_LEVEL
$previousInterval = $env:AGENT_HEARTBEAT_INTERVAL
try {
    $env:AGENT_LOG_LEVEL = 'debug'
    $env:AGENT_HEARTBEAT_INTERVAL = '2s'
    .\bin\agent.exe
} finally {
    $env:AGENT_LOG_LEVEL = $previousLevel
    $env:AGENT_HEARTBEAT_INTERVAL = $previousInterval
}
```

Ожидается `agent started`, затем примерно каждые 2 секунды — строка
`level=DEBUG msg="agent heartbeat"`. Нажми Ctrl+C: появляются сообщения остановки,
heartbeat прекращается, управление возвращается терминалу.

### 1.5 Проверить формат JSON

```powershell
$previousFormat = $env:AGENT_LOG_FORMAT
try {
    $env:AGENT_LOG_FORMAT = 'json'
    .\bin\agent.exe -check-config
} finally {
    $env:AGENT_LOG_FORMAT = $previousFormat
}
```

Ожидается одна строка JSON с полями `level: "INFO"`, `msg: "configuration valid"`,
`app: "study-agent"` и `timezone`. Команда завершается сама.

### 1.6 Проверить отклонение неверной настройки

```powershell
$previousTimeout = $env:AGENT_SHUTDOWN_TIMEOUT
try {
    $env:AGENT_SHUTDOWN_TIMEOUT = '-1s'
    .\bin\agent.exe -check-config
    $LASTEXITCODE
} finally {
    $env:AGENT_SHUTDOWN_TIMEOUT = $previousTimeout
}
```

Ожидается `configuration error: shutdown_timeout must be a positive duration, for example 10s`
и код `1`. Это успешная проверка отказа: программа не должна запускаться с отрицательным тайм-аутом.

### 1.7 Запустить автоматические проверки

| Команда | Ожидаемый результат |
|---|---|
| `go test ./...` | `ok` для `internal/app` и `internal/config`; у `cmd/agent` — `[no test files]` |
| `go test -race ./...` | Тесты проходят, сообщений `WARNING: DATA RACE` нет; на Windows нужен GCC |
| `go vet ./...` | Нет диагностических сообщений, код 0 |
| `go mod verify` | `all modules verified` |
| `git check-ignore .env bin/agent.exe` | Выводятся `.env` и `bin/agent.exe` |

GCC из MSYS2 установлен на этом компьютере; детектор гонок уже запускался успешно.

Чтобы посмотреть отдельные сценарии конфигурации с названиями:

```powershell
go test -v ./internal/config
```

Ожидается `PASS`. Проверяются приоритет настроек, отсутствие необязательного `.env`, неверный YAML,
неизвестные/повторные поля, неверные интервалы и часовой пояс, сокрытие содержимого ошибочного `.env`.
Тесты приложения проверяют остановку работников и ограничение времени ожидания.

## Справочник настроек

Порядок приоритета: **`configs/agent.yaml` → `.env` → окружение процесса**; значение справа важнее.
`.env` читается из текущей рабочей папки, необязателен и исключён из Git. Пример — `.env.example`.
Если переопределения уже заданы, вывод проверок может отличаться от настроек репозитория.

| YAML | Переменная окружения | Значение по умолчанию в репозитории |
|---|---|---|
| `log.level` | `AGENT_LOG_LEVEL` | `info`; допустимы также `debug`, `warn`, `error` |
| `log.format` | `AGENT_LOG_FORMAT` | `text`; альтернатива — `json` |
| `timezone` | `AGENT_TIMEZONE` | `Europe/Moscow` |
| `shutdown_timeout` | `AGENT_SHUTDOWN_TIMEOUT` | `10s`, строго больше нуля |
| `heartbeat_interval` | `AGENT_HEARTBEAT_INTERVAL` | `30s`, строго больше нуля |

Другой файл настроек: `go run ./cmd/agent -config configs/agent.yaml -check-config`.
Приложение не выводит настройки и секреты целиком в журнал и не меняет окружение системы.

## Структура проекта

```text
cmd/agent/                 точка входа
internal/app/              жизненный цикл и фоновая работа; тесты рядом
internal/config/           чтение и проверка настроек; тесты рядом
configs/                   общие YAML-настройки
docs/DOCS.md               инструкции ассистенту по оформлению документов
docs/reference/            исходные требования, ГОСТы и шаблоны
docs/coursework/           наши ТЗ и будущая РПЗ
.vscode/                   задачи запуска из VS Code
bin/                       результаты сборки, игнорируются Git
README.md                  текущий стек, история и проверка работающих функций
ПЛАН_РАЗРАБОТКИ.md         этапы реализации и статусы
```

В корне остаются необходимые служебные файлы: `go.mod`/`go.sum`, `.gitignore`, `.editorconfig`,
`.env.example` и локальный `.env`. Последний ожидается программой именно из рабочей папки.

Задачи VS Code: `Терминал → Выполнить задачу` → `Agent: run`, `Agent: check config`,
`Agent: build`, `Agent: test`. Каждая запускается из корня проекта.
