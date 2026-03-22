-- SharePoint Audit Database Schema
-- Complete table definitions, relationships, and indexes

-- Run status tracking
CREATE TABLE IF NOT EXISTS run_status (
    id INTEGER PRIMARY KEY,
    full_export_completed BOOLEAN NOT NULL DEFAULT FALSE,
    created_at DATETIME NOT NULL DEFAULT (datetime('now','localtime')),
    updated_at DATETIME NOT NULL DEFAULT (datetime('now','localtime'))
);

-- Sites (SharePoint sites and OneDrive personal sites)
CREATE TABLE IF NOT EXISTS sites (
    site_id TEXT PRIMARY KEY,
    site_url TEXT NOT NULL,
    time_created TEXT,
    modified TEXT,
    title TEXT,
    template_name TEXT,
    created_by_email TEXT,
    group_id TEXT,
    last_activity_on TEXT,
    storage_used REAL,
    lock_state TEXT,
    created_at DATETIME NOT NULL DEFAULT (datetime('now','localtime')),
    updated_at DATETIME NOT NULL DEFAULT (datetime('now','localtime'))
);

-- User profiles from SharePoint User Profile Service
CREATE TABLE IF NOT EXISTS user_profiles (
    personal_url TEXT PRIMARY KEY,
    doc_id TEXT,
    aad_object_id TEXT,
    account_name TEXT,
    personal_site_instantiation_state TEXT,
    profile_sid TEXT,
    user_profile_guid TEXT,
    user_principal_name TEXT,
    last_modified_time TEXT,
    created_at DATETIME NOT NULL DEFAULT (datetime('now','localtime')),
    updated_at DATETIME NOT NULL DEFAULT (datetime('now','localtime'))
);

-- Site users (from Site User Information Lists)
CREATE TABLE IF NOT EXISTS site_users (
    id INTEGER,
    site_id TEXT,
    email TEXT,
    expiration TEXT,
    is_email_authentication_guest_user BOOLEAN,
    is_hidden_in_ui BOOLEAN,
    is_share_by_email_guest_user BOOLEAN,
    is_site_admin BOOLEAN,
    login_name TEXT,
    principal_type INTEGER,
    title TEXT,
    userid_name_id TEXT,
    userid_name_id_issuer TEXT,
    user_principal_name TEXT,
    updated TEXT,
    created_at DATETIME NOT NULL DEFAULT (datetime('now','localtime')),
    updated_at DATETIME NOT NULL DEFAULT (datetime('now','localtime')),
    PRIMARY KEY (id, site_id),
    FOREIGN KEY (site_id) REFERENCES sites(site_id)
);

-- Site groups (SharePoint groups within sites)
CREATE TABLE IF NOT EXISTS site_groups (
    id INTEGER NOT NULL,
    title TEXT NOT NULL,
    login_name TEXT NOT NULL,
    description TEXT,
    owner_title TEXT,
    site_id TEXT NOT NULL,
    updated TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT (datetime('now','localtime')),
    updated_at DATETIME NOT NULL DEFAULT (datetime('now','localtime')),
    PRIMARY KEY (id, site_id),
    FOREIGN KEY (site_id) REFERENCES sites(site_id)
);

-- Group members (users within site groups)
CREATE TABLE IF NOT EXISTS group_members (
    id INTEGER NOT NULL,
    title TEXT NOT NULL,
    login_name TEXT NOT NULL,
    email TEXT,
    principal_type INTEGER NOT NULL,
    user_principal_name TEXT,
    group_id INTEGER NOT NULL,
    site_id TEXT NOT NULL,
    updated TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT (datetime('now','localtime')),
    updated_at DATETIME NOT NULL DEFAULT (datetime('now','localtime')),
    PRIMARY KEY (id, group_id, site_id),
    FOREIGN KEY (group_id, site_id) REFERENCES site_groups(id, site_id)
);

-- Site outcomes (per-site failure tracking)
CREATE TABLE IF NOT EXISTS site_outcomes (
    site_url TEXT NOT NULL,
    site_id TEXT NOT NULL,
    processor TEXT NOT NULL,
    outcome_json TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT (datetime('now','localtime')),
    PRIMARY KEY (site_id, processor)
);
CREATE INDEX IF NOT EXISTS idx_site_outcomes_processor ON site_outcomes(processor);

-- Indexes for performance
CREATE INDEX IF NOT EXISTS idx_sites_site_url ON sites(site_url);
CREATE INDEX IF NOT EXISTS idx_sites_lock_state ON sites(lock_state);

CREATE INDEX IF NOT EXISTS idx_user_profiles_aad_object_id ON user_profiles(aad_object_id);
CREATE INDEX IF NOT EXISTS idx_user_profiles_profile_sid ON user_profiles(profile_sid);
CREATE INDEX IF NOT EXISTS idx_user_profiles_user_principal_name ON user_profiles(user_principal_name);

CREATE INDEX IF NOT EXISTS idx_site_users_is_site_admin ON site_users(is_site_admin);
CREATE INDEX IF NOT EXISTS idx_site_users_login_name ON site_users(login_name);
CREATE INDEX IF NOT EXISTS idx_site_users_userid_name_id ON site_users(userid_name_id);
CREATE INDEX IF NOT EXISTS idx_site_users_user_principal_name ON site_users(user_principal_name);
CREATE INDEX IF NOT EXISTS idx_site_users_site_id ON site_users(site_id);

CREATE INDEX IF NOT EXISTS idx_site_groups_site_id ON site_groups(site_id);

CREATE INDEX IF NOT EXISTS idx_group_members_group_site ON group_members(group_id, site_id);
CREATE INDEX IF NOT EXISTS idx_group_members_site_id ON group_members(site_id);