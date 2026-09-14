-- +goose Up
-- Data migration, no schema change. Okta becomes a single provider keyed
-- by the immutable Okta user id, which arrives both as the persistent SAML
-- NameID and as SCIM externalId. Legacy 'okta' rows carried an email
-- subject from the retired sign-in email fallback and are invalid under
-- the new model; 'okta-scim' rows already hold the stable id and simply
-- take over the provider name. Delete first: a user may have both rows and
-- (user_id, provider) is unique.
DELETE FROM user_identities WHERE provider = 'okta';
UPDATE user_identities SET provider = 'okta' WHERE provider = 'okta-scim';

-- +goose Down
-- Reverts the provider rename only. The legacy email-subject rows deleted
-- by Up are not restored: they belonged to the retired sign-in fallback
-- and nothing depends on them, so this rollback preserves behaviour but
-- not those rows.
UPDATE user_identities SET provider = 'okta-scim' WHERE provider = 'okta';
