# add-scim-provisioning — Tasks

## 1. Основания

- [ ] 1.1 Конфигурация `SCIM_TOKEN` (опционален; при наличии — минимум 32 символа, иначе ошибка старта); unit-тесты: отсутствие допустимо, короткий токен отклоняется
- [ ] 1.2 `session.Manager.DestroyAllForUser`: обход per-user индекса, удаление всех токенов и индекса; интеграционный тест из спеки web-sessions (несколько сессий → все 401, индекс пуст)
- [ ] 1.3 Методы `internal/postgres`: `FindByEmailAny`, `CreateUser`, `UpdateUser`, `SetActive`, `ReplaceIdentity`; константа провайдера `okta-scim`; интеграционные тесты, включая 409-путь (unique violation) и смену externalId

## 2. Протокол SCIM

- [ ] 2.1 Пакет `internal/scim`: типы ресурсов/ошибок, Bearer-аутентификация (constant-time), discovery-хендлеры; тесты: 401 с неверным токеном, корректный ServiceProviderConfig
- [ ] 2.2 `POST /scim/v2/Users` и `GET /scim/v2/Users/{id}`: создание с identity okta-scim, 201/404/409; тесты по спеке (включая дубликат email в другом регистре и отсутствие ролей у нового пользователя)
- [ ] 2.3 `GET /scim/v2/Users` с фильтрами `userName eq` / `externalId eq`, пагинацией и честным totalResults; тесты: поиск в другом регистре, пустой результат, невалидный фильтр → invalidFilter
- [ ] 2.4 `PUT` и `PATCH` (replace с path и без): обновление профиля с сохранением UUID и назначений, атомарность, invalidPath/invalidValue; тесты по спеке

## 3. Деактивация

- [ ] 3.1 Общий helper деактивации/реактивации: `SetActive` + `DestroyAllForUser` + структурный лог; DELETE → 204 мягкое удаление; тесты: сессии мертвы сразу (`/auth/me` 401 без ожидания TTL), назначения сохранены, реактивация без восстановления сессий

## 4. Wiring и завершение

- [ ] 4.1 Монтирование `/scim/v2/` вне session-middleware только при заданном токене; без токена — 404; smoke на живом сервере
- [ ] 4.2 Прогнать `mise run lint`, `mise run test`, `mise run vuln`; все чистые
- [ ] 4.3 README: раздел SCIM (эндпоинты, `SCIM_TOKEN`, подключение Okta, что Groups не поддерживаются); вычитать на соответствие
