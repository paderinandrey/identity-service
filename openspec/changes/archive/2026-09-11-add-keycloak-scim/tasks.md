# add-keycloak-scim — Tasks

## 1. Провижининг из Keycloak

- [x] 1.1 Dockerfile для keycloak с jar плагина; compose build; realm: event listener scim + федерационный провайдер (BEARER, только users)
- [x] 1.2 Живая проверка: пользователь, созданный в Keycloak, появляется у нас через SCIM (identity okta-scim, событие в outbox) и входит по паролю через SAML
- [x] 1.3 README: обновить сценарий локального SSO; закоммитить и запушить
