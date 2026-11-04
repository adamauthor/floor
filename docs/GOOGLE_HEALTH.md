# Google Health: шаги и сон Fitbit без iPhone

`HEALTH_SOURCE=google`: бот раз в 15 минут сам забирает шаги и сон из [Google Health API](https://developers.google.com/health). Это преемник Fitbit Web API, который Google отключает 2 ноября 2026 года. Данные идут из облака Fitbit напрямую, Apple «Здоровье» и Команда iOS не нужны.

Настраивается один раз, примерно 10 минут.

## 0. Приложение Google Health

Войди в мобильное приложение Fitbit / Google Health своим Google-аккаунтом и перенеси профиль, если приложение это предлагает. Без профиля Google Health вход в API не пройдёт.

## 1. Проект в Google Cloud

1. Открой [console.cloud.google.com](https://console.cloud.google.com/) и создай проект, например `floor`.
2. **APIs & Services → Library** → найди **Google Health API** → **Enable**.
3. **Google Auth Platform → Branding:** имя приложения `Floor`, твоя почта как контактная. Остальное можно не заполнять.
4. **Audience:** тип пользователей **External**. В **Test users** добавь свой Google-аккаунт.
5. **Data Access → Add or remove scopes:** отметь
   - `.../auth/googlehealth.activity_and_fitness.readonly`
   - `.../auth/googlehealth.sleep.readonly`
6. **Clients → Create client:** тип **Desktop app**, имя `floor`. Скопируй **Client ID** и **Client secret**.

## 2. Не дать токену протухнуть через 7 дней

В режиме **Testing** Google отзывает доступ каждые 7 дней. Чтобы этого не было, на странице **Audience** нажми **Publish app** (статус **In production**).

Для публикации Branding требует ссылки. Это страницы `docs/index.html`, `docs/privacy/`, `docs/terms/`: GitHub Pages раздаёт папку `docs/` из `main`.

- Application home page: `https://adamauthor.github.io/floor/`
- Privacy policy: `https://adamauthor.github.io/floor/privacy/`
- Terms of service: `https://adamauthor.github.io/floor/terms/`
- Authorized domains: `adamauthor.github.io`

Логотип не загружай: с ним опубликованное приложение придётся отправлять на проверку.

Проверку приложения Google проходить не нужно: непроверенное приложение работает до 100 пользователей. При входе Google покажет экран «Приложение не проверено». Нажми **Дополнительно → Перейти на страницу Floor**: это твоё собственное приложение.

## 3. Ключи в `.env`

```
HEALTH_SOURCE=google
GOOGLE_CLIENT_ID=…apps.googleusercontent.com
GOOGLE_CLIENT_SECRET=…
```

## 4. Вход

```bash
make google-auth
```

Откроется браузер. Войди тем аккаунтом, к которому привязан Fitbit, и разреши доступ. Токен сохранится в `~/.floor/google-token.json` с правами 600. Команда сразу покажет шаги за последние дни: так видно, что данные читаются.

После этого `make install-launchd`. Если доступ когда-нибудь пропадёт, бот напишет в Telegram, что пора снова запустить `make google-auth`.

## Как данные переводятся

| Google Health | Floor |
|---|---|
| `steps` → `dailyRollUp` по местным дням | шаги за дату; дни без синхронизации пропускаются, а не записываются нулём |
| `sleep`, стадии `LIGHT` / `DEEP` / `REM` / `ASLEEP` | сон |
| `sleep`, стадии `AWAKE` / `RESTLESS` | не сон (как у Fitbit: беспокойный сон не входит в «minutes asleep») |
| `sleep` без стадий | весь интервал считается сном |

Забираются последние 5 дней. Повтор тех же данных бот пропускает.
