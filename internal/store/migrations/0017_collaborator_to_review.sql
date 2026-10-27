-- Pocket Collaborator is now Pocket Review (slug "review"). Carry the registered
-- app over in place so its pca_ token, OIDC client secret, links, per-person
-- switches and event history keep working; the app re-registers with the new
-- slug and fills in the rest (name, URLs, manifest).
UPDATE apps SET slug = 'review', name = 'Pocket Review', env_prefix = 'REVIEW'
 WHERE slug = 'collaborator' AND NOT EXISTS (SELECT 1 FROM apps WHERE slug = 'review');
UPDATE services SET type = 'review', name = CASE WHEN name = 'Pocket Collaborator' THEN 'Pocket Review' ELSE name END
 WHERE type = 'collaborator';
UPDATE oidc_clients SET client_id = 'pocket-review', name = 'Pocket Review'
 WHERE client_id = 'pocket-collaborator' AND NOT EXISTS (SELECT 1 FROM oidc_clients WHERE client_id = 'pocket-review');
UPDATE oidc_tokens SET client_id = 'pocket-review' WHERE client_id = 'pocket-collaborator';
UPDATE app_links SET target_slug = 'review', reason = replace(reason, 'Pocket Collaborator', 'Pocket Review')
 WHERE target_slug = 'collaborator';
UPDATE link_tokens SET target_slug = 'review' WHERE target_slug = 'collaborator';
UPDATE link_modes SET target = 'review' WHERE target = 'collaborator';
UPDATE events SET topic = 'review.' || substr(topic, 14) WHERE topic LIKE 'collaborator.%';
UPDATE events SET source = 'review' WHERE source = 'collaborator';
UPDATE event_receipts SET source = 'review' WHERE source = 'collaborator';
UPDATE webhooks SET topics = replace(topics, 'collaborator.', 'review.') WHERE topics LIKE '%collaborator.%';
