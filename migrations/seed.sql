-- seed.sql
-- Development Seed Data for FlowForge
-- WARNING: FOR LOCAL DEVELOPMENT USE ONLY. DO NOT EXECUTE IN PRODUCTION.
-- Default user: admin@flowforge.local / secret123


INSERT INTO tenants (id, slug, name)
VALUES ('1d1c8e4d-0a14-4f51-b8b0-2e5d5b74d2ff', 'default-tenant', 'Default Workspace')
ON CONFLICT (slug) DO NOTHING;

INSERT INTO users (id, tenant_id, email, password_hash, role, is_active)
VALUES (
    'b4b4f5d2-7f4e-4c0a-9cf3-3f8f8a2f1b11',
    '1d1c8e4d-0a14-4f51-b8b0-2e5d5b74d2ff',
    'admin@flowforge.local',
    '$2a$10$7EqJtq986P4X.B5k2v0/2.3zW9yK4T4M3nL7U5zX5Y5Z5a5b5c5d5e', -- secret123 hashed placeholder
    'admin',
    TRUE
)
ON CONFLICT (tenant_id, email) DO NOTHING;
