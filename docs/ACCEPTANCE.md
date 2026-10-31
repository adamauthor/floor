# Приёмка

Состояние критериев из [ТЗ, раздел 14](SPEC.md#14-критерии-приёмки) на 31 октября 2026.

| Критерий | Автотест | На реальном телефоне и Telegram |
|---|---|---|
| Выгрузка попадает в БД в течение минуты | ✅ `TestEndToEnd`, `TestFileSource` | ⏳ нужна Команда на iPhone (M0) |
| Утро приходит один раз с корректными цифрами | ✅ `TestMorningOnceWithCorrectNumbers` | ⏳ |
| Без данных утро приходит в якорь + 3 ч | ✅ `TestMorningWithoutDataComesAtFallback` | ⏳ |
| Вечерняя кнопка: один тап, правка вместо дубля | ✅ `TestEveningButtonSavesAndEdits`, `TestEndToEnd` | ⏳ |
| Два пропуска дают строку про «только пол» | ✅ `TestTwoMissesInARow` | ⏳ |
| Смена пояса без двойных отправок | ✅ `TestTimezoneChangeMovesScheduleWithoutDoubles` | ⏳ |
| Неделя в воскресенье, таблица совпадает с БД | ✅ `TestWeeklyOnSundayMatchesDB` | ⏳ |
| Без Claude всё работает на fallback | ✅ `TestAllScenariosWorkWithoutClaude` | ⏳ |
| Чужие аккаунты игнорируются | ✅ `TestStrangersIgnoredSilently` | ⏳ |
| Перезапуск без повторных отправок | ✅ `TestRestartDoesNotResend` | ⏳ |
| Карточка привычек закреплена и обновляется | ✅ `TestHabitCardPinnedOnceAndUpdated`, `TestHabitCardRepinnedWhenDeleted` | ⏳ |

Также проверено вручную: `claude -p` с теми же флагами (`TestLiveClaude`), сборка и запуск Docker-образа, команды `floor habits` и `floor debug-day`, синтаксис launchd plist.

**Не проверено:** настоящий Telegram (нужен токен от @BotFather), Команда iOS и Fitbit Air, установка launchd-агента и доступ к iCloud Drive из него, деплой на Fly.io. `TestEndToEnd` гоняет настоящую библиотеку Telegram против поддельного сервера, поэтому протокол проверен только в той мере, в какой поддельный сервер похож на настоящий.

```bash
go test -race ./...
```
