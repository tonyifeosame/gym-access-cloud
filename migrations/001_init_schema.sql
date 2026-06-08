-- Initial schema for Gym Access Cloud API
-- This migration creates all required tables for the system

-- Sites table: Stores gym locations and their API keys
CREATE TABLE IF NOT EXISTS sites (
    id SERIAL PRIMARY KEY,
    site_name VARCHAR(100) UNIQUE NOT NULL,
    api_key VARCHAR(255) UNIQUE NOT NULL,
    active BOOLEAN DEFAULT TRUE,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- Members table: Stores member information and fingerprint templates
CREATE TABLE IF NOT EXISTS members (
    id SERIAL PRIMARY KEY,
    member_id VARCHAR(50) UNIQUE NOT NULL,
    full_name TEXT NOT NULL,
    membership_type TEXT NOT NULL,
    active BOOLEAN DEFAULT TRUE,
    fingerprint_template TEXT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- Enrollment requests table: Tracks fingerprint enrollment process
CREATE TABLE IF NOT EXISTS enrollment_requests (
    id SERIAL PRIMARY KEY,
    member_id VARCHAR(50) NOT NULL,
    status VARCHAR(20) NOT NULL DEFAULT 'PENDING',
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    completed_at TIMESTAMP,
    FOREIGN KEY (member_id) REFERENCES members(member_id) ON DELETE CASCADE
);

-- Access logs table: Records all access attempts
CREATE TABLE IF NOT EXISTS access_logs (
    id SERIAL PRIMARY KEY,
    member_id VARCHAR(50),
    granted BOOLEAN NOT NULL,
    source VARCHAR(50) NOT NULL,
    site_name VARCHAR(100) NOT NULL,
    message TEXT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (member_id) REFERENCES members(member_id) ON DELETE SET NULL
);

-- Indexes for performance
CREATE INDEX IF NOT EXISTS idx_members_member_id ON members(member_id);
CREATE INDEX IF NOT EXISTS idx_members_active ON members(active);
CREATE INDEX IF NOT EXISTS idx_members_updated_at ON members(updated_at);
CREATE INDEX IF NOT EXISTS idx_enrollment_requests_member_id ON enrollment_requests(member_id);
CREATE INDEX IF NOT EXISTS idx_enrollment_requests_status ON enrollment_requests(status);
CREATE INDEX IF NOT EXISTS idx_access_logs_member_id ON access_logs(member_id);
CREATE INDEX IF NOT EXISTS idx_access_logs_created_at ON access_logs(created_at);
CREATE INDEX IF NOT EXISTS idx_access_logs_site_name ON access_logs(site_name);

-- Insert default sites (these should be changed in production)
INSERT INTO sites (site_name, api_key, active) VALUES
    ('Main Gym', 'main-gym-api-key-123', TRUE),
    ('Lekki Branch', 'lekki-branch-api-key-456', TRUE),
    ('Abuja Branch', 'abuja-branch-api-key-789', TRUE)
ON CONFLICT (site_name) DO NOTHING;

-- Function to update updated_at timestamp
CREATE OR REPLACE FUNCTION update_updated_at_column()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = CURRENT_TIMESTAMP;
    RETURN NEW;
END;
$$ language 'plpgsql';

-- Triggers to auto-update updated_at
CREATE TRIGGER update_members_updated_at BEFORE UPDATE ON members
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

CREATE TRIGGER update_sites_updated_at BEFORE UPDATE ON sites
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();
