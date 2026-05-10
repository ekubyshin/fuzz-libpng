# libpng + AFL++ (лабораторная)

## Что нужно

Linux (проще всего Ubuntu), [AFL++](https://github.com/AFLplusplus/AFLplusplus) из исходников, в `PATH` есть **`afl-clang-fast`** и **`afl-fuzz`** (не пакет из `apt`, если он старый).

Для **FormatFuzzer** нужен отдельный форк **`afl-fuzz`**: [uds-se/AFLplusplus](https://github.com/uds-se/AFLplusplus) — соберите его и поставьте **в начало `PATH`**, чтобы именно он запускался (`which afl-fuzz`). Обычный AFLplusplus/AFLplusplus и `png.so` из FormatFuzzer часто **несовместимы**.

## Сборка

```bash
chmod +x build.sh
./build.sh
```

Получите бинарь **`png_read_harness`**, каталоги **`install/`** и **`third_party/`**, файл **`corpus/seed_minimal.png`**.

## Фаззинг

Обычный AFL++ (без внешнего мутатора):

```bash
unset AFL_CUSTOM_MUTATOR_LIBRARY AFL_FFGEN
afl-fuzz -i corpus -o out -- ./png_read_harness @@
```

С [FormatFuzzer](https://github.com/uds-se/FormatFuzzer): в каталоге FormatFuzzer **`./build.sh png`** (должен появиться **`png.so`**). Затем — только **`afl-fuzz`** из [uds-se/AFLplusplus](https://github.com/uds-se/AFLplusplus):

```bash
nm -D /полный/путь/FormatFuzzer/png.so | grep afl_custom_init   # должна быть строка T afl_custom_init
export AFL_CUSTOM_MUTATOR_LIBRARY=/полный/путь/FormatFuzzer/png.so
afl-fuzz -i corpus -o out-ff -- ./png_read_harness @@
```

Если **`nm` ничего не показывает** — это не тот файл, неполная сборка или обрезаны символы; пересоберите `png.so` по README FormatFuzzer.

## Ошибка про `core_pattern` (Ubuntu/VM)

Если `afl-fuzz` пишет про **Pipe at the beginning of 'core_pattern'** и выходит — ядро шлёт коры в `apport`/`systemd-coredump`, и AFL так не хочет работать.

**Вариант А (правильно для лабы):** один раз под root:

```bash
echo core | sudo tee /proc/sys/kernel/core_pattern
```

**Вариант Б (только поиграться):** перед `afl-fuzz`:

```bash
export AFL_I_DONT_CARE_ABOUT_MISSING_CRASHES=1
```

После перезагрузки ВМ настройка `core_pattern` может сброситься — снова выполните вариант А при необходимости.

## `Symbol 'afl_custom_init' not found`

Значит, загружаемая библиотека **не экспортирует API кастомного мутатора AFL++**. Чаще всего:

1. В **`PATH`** стоит **`afl-fuzz`** из **другого** AFL++ (например upstream или из `apt`), а **`png.so`** собран под **uds-se/AFLplusplus** — поставьте собранный вручную `afl-fuzz` первым в `PATH` (`which -a afl-fuzz`).
2. В **`AFL_CUSTOM_MUTATOR_LIBRARY`** указан **не тот** `.so` (не из `FormatFuzzer` после `./build.sh png`).
3. Проверка: **`nm -D …/png.so | grep afl_custom`** — должны быть хотя бы **`afl_custom_init`** и **`afl_custom_fuzz`**.

## Сдача

Кратко опишите шаги, сравните два прогона, приложите краши/графики — шаблон в **[REPORT.md](REPORT.md)**. Идеи, где искать баги: [issues libpng](https://github.com/pnggroup/libpng/issues), старые теги под известные CVE.
