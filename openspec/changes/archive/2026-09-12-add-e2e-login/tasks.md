# add-e2e-login — Tasks

## 1. Реализация

- [x] 1.1 Конфигурация `E2E_LOGIN_TOKEN` (опционален, ≥32 символов; в production задан → ошибка старта); unit-тесты
- [x] 1.2 Хендлер `POST /internal/e2e/login` в `internal/session`: Bearer constant-time, поиск активного пользователя по email, `Manager.Start` без TouchLastSignIn и без sign-in метрик; монтирование за session-middleware только при заданном токене
- [x] 1.3 Интеграционные тесты по спеке: успех + `/auth/me`, неверный токен, неактивный пользователь, 404 без токена, метрики/last_sign_in_at не тронуты
- [x] 1.4 README: раздел для фронтовых E2E (пример Playwright global-setup + storageState, домен cookie, smoke через IdP); `mise run lint/test/vuln` чистые
