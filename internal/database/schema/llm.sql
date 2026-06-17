-- LLM connection management tables

CREATE TABLE IF NOT EXISTS llm_connections (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL,
    provider_type TEXT NOT NULL,
    model TEXT NOT NULL,
    api_key_encrypted TEXT,
    base_url TEXT,
    provider_config TEXT,
    is_default BOOLEAN NOT NULL DEFAULT 0,
    is_enabled BOOLEAN NOT NULL DEFAULT 1,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS llm_provider_model_cache (
    provider_type     TEXT PRIMARY KEY,
    models_json       TEXT NOT NULL,
    last_refreshed_at DATETIME,
    last_error        TEXT,
    updated_at        DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
