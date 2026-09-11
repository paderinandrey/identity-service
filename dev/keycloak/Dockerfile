FROM quay.io/keycloak/keycloak:26.7

# SCIM 2.0 client plugin: propagates Keycloak user changes to external
# SCIM servers (mirrors Okta provisioning locally).
# https://github.com/mitodl/keycloak-scim
ADD --chown=keycloak:keycloak \
    https://github.com/mitodl/keycloak-scim/releases/download/latest/keycloak-scim-1.0-SNAPSHOT-all.jar \
    /opt/keycloak/providers/keycloak-scim.jar
