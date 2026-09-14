# harden-session-revocation — Tasks

## 1. Поколение в данных

- [x] 1.1 Миграция `00008`: `users.session_epoch bigint NOT NULL DEFAULT 0` с комментарием; `identity.User.SessionEpoch`, `userColumns`/`scanUser`
- [x] 1.2 `SetActive` поднимает поколение при переходе в `false` в той же транзакции и возвращает обновлённого пользователя; тест: поколение растёт при деактивации и не меняется при реактивации
- [x] 1.3 `EffectivePermissions` фильтрует неактивных; тест на пустой набор

## 2. Сессии

- [x] 2.1 `Manager.Start(ctx, userID, epoch)` кладёт поколение в сессию и пишет ограждение `user_epoch:<uid>`; `Manager.SessionEpoch(ctx)`; вызовы из `samlsso` и `e2elogin`
- [x] 2.2 Обёртка хранилища: `FindCtx` и `CommitCtx` сравнивают поколение сессии с ограждением; устаревшая сессия не загружается и не сохраняется; ошибка Redis → ошибка scs → 503
- [x] 2.3 `DestroyAllForUser(ctx, userID, epoch)`: ограждение до удаления по индексу
- [x] 2.4 `currentUser` (session) и `authMiddleware` (graphql): поколение сессии ≠ `user.SessionEpoch` → нет пользователя
- [x] 2.5 Тесты session: запрос в полёте не восстанавливает сессию (сценарий Codex, настоящий Redis); после отзыва все токены 401; новый вход после отзыва работает; сессия без поколения валидна при поколении 0 и отклоняется после отзыва; ограждение выставлено раньше удаления

## 3. Провижининг и наблюдаемость

- [x] 3.1 SCIM `deactivate`: `SetActive` → `DestroyAllForUser` с новым поколением; отказ Redis — лог + метрика `session_revocation_errors_total`, ответ успешный; `reactivate` поколение не трогает
- [x] 3.2 Тесты scim: отказ `SessionKiller` → 200, после реактивации старый cookie → 401 (сценарий Codex); обычная деактивация → 401 сразу
- [x] 3.3 README (поведение отзыва, новая метрика); `mise run lint`, `mise run test`, `mise run vuln` чистые; `stand:verify` зелёный; CI зелёный
