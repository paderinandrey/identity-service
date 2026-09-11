# add-keycloak-dev — Tasks

## 1. Keycloak

- [x] 1.1 Сервис keycloak в compose (профиль sso, healthcheck, импорт realm) + realm-файл + mise-таск `up-sso`
- [x] 1.2 Проверить полный SAML-цикл вживую: init → форма Keycloak → пароль → ACS → cookie → `/auth/me` 200
- [x] 1.3 README: раздел локального SSO; закоммитить и запушить
