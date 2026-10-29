-- M1 scope reduction: emergency access (breakglass) was removed from the
-- product. Drop its grants table and retire its permission. Migrations are
-- immutable once applied, so this is a new forward migration instead of an
-- edit to 0003.

DROP TABLE IF EXISTS breakglass_grants;

DELETE FROM role_permissions
WHERE permission_id IN (SELECT id FROM permissions WHERE code = 'admin.breakglass');

DELETE FROM permissions WHERE code = 'admin.breakglass';
