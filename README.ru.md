<div align="center">

# ollamacmf

**Ollama с поддержкой моделей CMF.**

[![Release](https://img.shields.io/github/v/release/infosave2007/ollamacmf?label=release)](https://github.com/infosave2007/ollamacmf/releases/latest)
[![Build](https://github.com/infosave2007/ollamacmf/actions/workflows/release-ollamacmf.yml/badge.svg)](https://github.com/infosave2007/ollamacmf/actions/workflows/release-ollamacmf.yml)
[![CMF models](https://img.shields.io/badge/CMF%20models-Hugging%20Face-ffd21e?logo=huggingface)](https://huggingface.co/infosave)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

[Установка](#установка) · [Быстрый старт](#быстрый-старт) · [Модели](#модели) · [Из приложений](#использование-из-приложений) · [Настройка](#настройка) · [Проблемы](#если-что-то-не-работает) · [English](README.md)

</div>

`ollamacmf` — это форк [Ollama](https://github.com/ollama/ollama), который умеет запускать модели в формате [CMF](https://github.com/infosave2007/cmf): компактный однофайловый формат, который создаёт Cortiq. Всё, что вы знаете про Ollama, работает так же: `pull`, `run`, `list`, `rm`, REST API, OpenAI‑совместимый API и любые приложения, которые умеют общаться с Ollama.

```sh
ollamacmf run hf.co/infosave/Granite-4.2-cmf:granite-4.2-3b-q4tp
```

Рантайм Cortiq, который исполняет CMF‑модели, скачивается, проверяется и устанавливается автоматически при первом запуске. Настраивать ничего не нужно.

## Установка

**macOS и Linux**

```sh
curl -fsSL https://raw.githubusercontent.com/infosave2007/ollamacmf/main/install.sh | sh
```

**Windows** (PowerShell)

```powershell
irm https://raw.githubusercontent.com/infosave2007/ollamacmf/main/install.ps1 | iex
```

**Вручную.** Скачайте архив для своей платформы со страницы [последнего релиза](https://github.com/infosave2007/ollamacmf/releases/latest), распакуйте куда угодно и запускайте `ollamacmf` оттуда. В каждом архиве лежит бинарник `ollamacmf` и подходящий рантайм Cortiq.

**Из исходников** (Go 1.26 или новее, компилятор C)

```sh
git clone https://github.com/infosave2007/ollamacmf.git
cd ollamacmf
go build -o ollamacmf .
```

`ollamacmf` может жить рядом с обычной Ollama. Порт по умолчанию у них общий (`11434`), поэтому либо остановите одну из них, либо дайте `ollamacmf` свой порт: `OLLAMA_HOST=127.0.0.1:11435`.

## Быстрый старт

В одном терминале запустите сервер:

```sh
ollamacmf serve
```

В другом — поговорите с моделью:

```sh
ollamacmf run hf.co/infosave/LFM2.5-cmf:lfm2.5-230m-q4tp
```

При первом запуске скачается рантайм Cortiq (около 16 МБ) и модель (126 МБ для этой), после чего откроется чат. Выйти — `/bye`.

Проверить, что всё на месте, можно в любой момент:

```sh
ollamacmf cmf status
```

```text
Server:    running at http://127.0.0.1:11434 (ollamacmf 0.1.0)
Platform:  darwin/arm64
Runtime:   Cortiq installed (0.8.19, managed)
           /Users/you/.ollama/cortiq/v0.8.19/cortiq
Expected:  Cortiq v0.8.19 in /Users/you/.ollama/cortiq

CMF models:
NAME                                          ARCH    PARAMS     QUANT       SIZE
hf.co/infosave/LFM2.5-cmf:lfm2.5-230m-q4tp    lfm2    229.69M    q4_block    132 MB
```

## Модели

### Где искать

CMF‑модели опубликованы на Hugging Face: [huggingface.co/infosave](https://huggingface.co/infosave). Посмотреть список из терминала:

```sh
ollamacmf cmf search            # все
ollamacmf cmf search granite    # по названию
```

Подборка с размерами — в [docs/cmf-models.md](docs/cmf-models.md).

### Скачать с Hugging Face

Репозиторий указывается как `hf.co/<аккаунт>/<репозиторий>`. Если в нём один CMF‑файл, больше ничего не нужно:

```sh
ollamacmf pull hf.co/infosave/Bonsai-1.7Bcmf
```

Если вариантов несколько, выберите тегом. Тег — это имя файла без `.cmf`; подойдёт и уникальный фрагмент имени:

```sh
ollamacmf pull hf.co/infosave/Granite-4.2-cmf:granite-4.2-3b-q4tp
ollamacmf pull hf.co/infosave/Granite-4.2-cmf:8b-q4tp
```

Если тег не указан или неоднозначен, `ollamacmf` покажет доступные файлы с размерами и готовую команду для каждого. Загрузка докачивается после обрыва, каждый файл сверяется с SHA‑256 перед регистрацией. Для приватных и gated‑репозиториев задайте `HF_TOKEN`.

### Локальный файл

Уже есть `.cmf`, например после `cortiq convert`? Запускайте напрямую:

```sh
ollamacmf run ./granite-4.2-3b-q4tp.cmf
```

или зарегистрируйте под своим именем:

```sh
ollamacmf cmf import ./granite-4.2-3b-q4tp.cmf --name granite
ollamacmf run granite
```

Принимаются только текстовые генеративные модели. Файлы для изображений, видео, звука, эмбеддингов и decision‑модели отклоняются с понятным сообщением.

### Управление

Работают привычные команды Ollama:

```sh
ollamacmf list                 # установленные модели
ollamacmf show granite         # архитектура, параметры, контекст, квантование, возможности
ollamacmf ps                   # загруженные модели
ollamacmf stop granite         # выгрузить сейчас
ollamacmf cp granite my-granite
ollamacmf rm granite
```

### Настройка через Modelfile

Производная модель с системным промптом или другими параметрами создаётся как в Ollama:

```text
FROM hf.co/infosave/Granite-4.2-cmf:granite-4.2-3b-q4tp
SYSTEM Ты лаконичный помощник. Отвечай одним абзацем.
PARAMETER temperature 0.3
PARAMETER num_predict 512
```

```sh
ollamacmf create concise-granite -f Modelfile
```

`FROM` может указывать и прямо на `.cmf`‑файл. `TEMPLATE`, `RENDERER` и `PARSER` не используются: шаблон чата и токенизатор лежат внутри CMF‑файла.

## Использование из приложений

`ollamacmf` отдаёт стандартный API Ollama и OpenAI‑совместимый API, поэтому существующие клиенты работают без изменений. Адрес — `http://localhost:11434`.

**Ollama API**

```sh
curl http://localhost:11434/api/chat -d '{
  "model": "hf.co/infosave/Granite-4.2-cmf:granite-4.2-3b-q4tp",
  "messages": [{"role": "user", "content": "Почему небо голубое?"}]
}'
```

**OpenAI SDK**

```python
from openai import OpenAI

client = OpenAI(base_url="http://localhost:11434/v1", api_key="ollama")
reply = client.chat.completions.create(
    model="hf.co/infosave/Granite-4.2-cmf:granite-4.2-3b-q4tp",
    messages=[{"role": "user", "content": "Почему небо голубое?"}],
)
print(reply.choices[0].message.content)
```

**Open WebUI, Continue, Zed, Cline и другие** подключаются так же, как к Ollama. Имена моделей для ввода покажет `ollamacmf list`.

### Что поддерживается

| Возможность | Статус |
| --- | --- |
| Чат и генерация, стриминг | Да |
| Вызов инструментов (tools) | Да, если шаблон чата модели поддерживает инструменты (`ollamacmf show` покажет `tools`) |
| Режим рассуждений (thinking) | Да, если модель умеет (`ollamacmf show` покажет `thinking`); `--think=false` выключает |
| `temperature`, `top_p`, `seed`, `num_predict`, `stop` | Да |
| `top_k`, `min_p`, штрафы за повторы, `num_ctx`, `num_gpu` | Принимаются и игнорируются: сэмплингом и контекстом управляет Cortiq |
| Структурированный вывод (`format`), log probabilities | Пока нет |
| Изображения и звук на входе, эмбеддинги | Пока нет |
| `ollamacmf push` на ollama.com | Нет. Делитесь `.cmf`‑файлом или публикуйте на Hugging Face |

Модели GGUF и safetensors работают как в upstream Ollama, если собраны соответствующие нативные раннеры; обычный `go build` даёт бинарник, который обслуживает только CMF.

## Настройка

У всего есть разумные значения по умолчанию. Переменные ниже читает `ollamacmf serve`.

| Переменная | Назначение | По умолчанию |
| --- | --- | --- |
| `OLLAMA_HOST` | Адрес, который слушает сервер | `127.0.0.1:11434` |
| `OLLAMA_MODELS` | Где хранятся модели | `~/.ollama/models` |
| `OLLAMA_KEEP_ALIVE` | Сколько модель остаётся в памяти после последнего запроса | `5m` |
| `OLLAMA_CORTIQ_DIR` | Куда устанавливается рантайм Cortiq | `cortiq` рядом с каталогом моделей |
| `OLLAMA_CORTIQ_PATH` | Использовать этот исполняемый файл Cortiq вместо управляемого | не задано |
| `OLLAMA_CORTIQ_AUTO_INSTALL` | Скачивать рантайм автоматически, если его нет (`0` — выключить) | `1` |
| `CMF_GPU` | `1` — просить у Cortiq GPU (Metal на Apple Silicon, Vulkan/DX12 на остальных), `0` — только CPU | по умолчанию Cortiq |
| `CMF_THREADS` | Число потоков CPU для Cortiq | все ядра |
| `HF_TOKEN` | Токен Hugging Face для приватных и gated‑репозиториев | не задано |

Переменные с префиксом `CMF_` передаются в Cortiq; полный список — в [документации Cortiq](https://github.com/infosave2007/cmf).

### Рантайм Cortiq

`ollamacmf` проверен с одним конкретным релизом Cortiq; он записан в [`cmfrunner/cmf.lock.json`](cmfrunner/cmf.lock.json) вместе с SHA‑256 каждой сборки. При первом использовании сервер скачивает этот релиз для вашей платформы, сверяет контрольную сумму и устанавливает в `~/.ollama/cortiq/<версия>/`. В релизных архивах тот же рантайм лежит рядом с бинарником, так что офлайн‑установка ничего не скачивает.

```sh
ollamacmf cmf setup     # установить рантайм сейчас, а не при первом запросе
ollamacmf cmf status    # показать, какой рантайм используется и откуда он
```

Порядок поиска: `OLLAMA_CORTIQ_PATH`, управляемая установка, `cortiq` рядом с `ollamacmf`, затем `cortiq` в `PATH`. Рантайм из `PATH` другой версии используется, но об этом пишется в лог сервера.

Поддерживаемые платформы: macOS (Apple Silicon и Intel), Linux x86‑64, Windows x86‑64 и ARM64. Для Linux ARM64 опубликованной сборки Cortiq пока нет: соберите Cortiq из исходников и укажите `OLLAMA_CORTIQ_PATH`.

## Если что-то не работает

**`CMF runtime is not available`** — сервер не нашёл и не смог скачать Cortiq. Посмотрите причину в `ollamacmf cmf status`, затем выполните `ollamacmf cmf setup` или укажите `OLLAMA_CORTIQ_PATH`. На машине без интернета скопируйте архив, указанный в `cmfrunner/cmf.lock.json`, и распакуйте его в каталог, который печатает `cmf status`.

**`the repository has several CMF files`** — добавьте тег, чтобы выбрать файл; сообщение перечисляет варианты.

**`this is a CMF image model`** (или audio, video, embedding, decision) — здесь запускаются только текстовые модели. Для остальных используйте Cortiq напрямую.

**Ответ обрывается** — увеличьте `num_predict` (в Modelfile или в опциях запроса). Без него `ollamacmf` разрешает до 32 768 токенов либо размер контекста модели, что меньше.

**Уже запущена другая Ollama** — обе используют порт 11434. Остановите одну или запустите `OLLAMA_HOST=127.0.0.1:11435 ollamacmf serve` и задайте тот же `OLLAMA_HOST` в терминале клиента.

**`ollamacmf ps` показывает `100% CPU`** — Cortiq сам выбирает устройство и не сообщает об этом обратно, поэтому для CMF‑моделей эта колонка не информативна. Какой бэкенд выбран, видно в логе сервера.

Лог сервера пишется в терминал с `ollamacmf serve`; `OLLAMA_DEBUG=1` делает его подробнее.

## Как это устроено

```text
ollamacmf CLI / Ollama API / OpenAI API
                 │
                 ▼
          сервер ollamacmf          хранилище моделей, планировщик, HTTP API
                 │
                 ▼
     cmfrunner → cortiq serve       один процесс Cortiq на загруженную модель,
                 │                  только loopback, сервер его запускает и останавливает
                 ▼
            model.cmf               веса, токенизатор и шаблон чата в одном файле
```

Cortiq отвечает за токенизацию, шаблон чата, сэмплинг и контекст; `ollamacmf` передаёт ему запросы и переводит ответы в форматы Ollama и OpenAI. Порт Cortiq выбирается на каждый процесс на `127.0.0.1` и не предназначен для прямого использования.

## Документация

- [Руководство пользователя](docs/cmf.md) (англ.) — установка, загрузка, импорт, настройка, API, диагностика подробнее
- [Каталог моделей](docs/cmf-models.md) — CMF‑модели на Hugging Face с размерами и командами
- [Заметки для мейнтейнеров](docs/cmf-engine.md) — как устроены адаптер, лок рантайма и релизный конвейер
- [Обновление рантайма](docs/cmf-release-automation.md) — как новый релиз Cortiq попадает в лок
- [Разработка](docs/development.md) — сборка нативных раннеров Ollama

## Участие и безопасность

Ошибки и идеи — в [issue tracker](https://github.com/infosave2007/ollamacmf/issues); см. [CONTRIBUTING.md](CONTRIBUTING.md). Об уязвимостях сообщайте приватно через [GitHub Security Advisories](https://github.com/infosave2007/ollamacmf/security/advisories/new), как описано в [SECURITY.md](SECURITY.md).

`ollamacmf` — независимый проект, не связанный с Ollama, Inc. Распространяется под [лицензией MIT](LICENSE), унаследованной от Ollama. Встроенный рантайм Cortiq распространяется под Apache‑2.0.
