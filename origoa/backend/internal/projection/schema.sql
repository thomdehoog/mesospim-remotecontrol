-- Origoa Foundation projection schema.
--
-- PostgreSQL stores derived repository information only. Every table in
-- this schema can be reconstructed entirely from the Git repository; the
-- database is never authoritative.

CREATE EXTENSION IF NOT EXISTS ltree;

-- Git revision represented by this projection.
CREATE TABLE IF NOT EXISTS repo_state (
    id             int  PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    processed_hash text NOT NULL DEFAULT ''
);
INSERT INTO repo_state (id) VALUES (1) ON CONFLICT DO NOTHING;

-- GUID resolution, hierarchy index and metadata projection for the four
-- native artifact kinds (entry, document, link, comment).
CREATE TABLE IF NOT EXISTS artifacts (
    guid           uuid  PRIMARY KEY,
    kind           text  NOT NULL,
    type           text  NOT NULL DEFAULT '',
    title          text  NOT NULL DEFAULT '',
    hid            text  NOT NULL DEFAULT '',
    repo_path      text  NOT NULL,             -- storage location (GUID directory or metadata file)
    parent_path    text  NOT NULL DEFAULT '',  -- enclosing folder
    ltpath         ltree NOT NULL,             -- encoded parent_path for subtree queries
    content        text  NOT NULL,             -- Git-faithful JSON (text, not jsonb: may carry an escaped NUL)
    updated_commit text  NOT NULL DEFAULT '',
    updated_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS artifacts_ltpath_gist ON artifacts USING gist (ltpath);
CREATE INDEX IF NOT EXISTS artifacts_kind_type   ON artifacts (kind, type);
CREATE UNIQUE INDEX IF NOT EXISTS artifacts_hid  ON artifacts (hid) WHERE hid <> '';

-- Schema-defined indexed key/value pairs for filtering.
CREATE TABLE IF NOT EXISTS field_index (
    guid  uuid NOT NULL,
    field text NOT NULL,
    value text NOT NULL,
    PRIMARY KEY (guid, field, value)
);
CREATE INDEX IF NOT EXISTS field_index_lookup ON field_index (field, value);

-- Full-text search projection.
CREATE TABLE IF NOT EXISTS fts (
    guid uuid PRIMARY KEY,
    tsv  tsvector NOT NULL
);
CREATE INDEX IF NOT EXISTS fts_gin ON fts USING gin (tsv);

-- Link summaries for efficient relationship navigation.
CREATE TABLE IF NOT EXISTS link_index (
    guid   uuid PRIMARY KEY,
    type   text NOT NULL DEFAULT '',
    source uuid,
    target uuid
);
CREATE INDEX IF NOT EXISTS link_index_source ON link_index (source);
CREATE INDEX IF NOT EXISTS link_index_target ON link_index (target);

-- Comment summaries (discussion threading and per-subject lookup).
CREATE TABLE IF NOT EXISTS comment_index (
    guid    uuid PRIMARY KEY,
    subject uuid,
    parent  uuid
);
CREATE INDEX IF NOT EXISTS comment_index_subject ON comment_index (subject);

-- History of assigned human-readable identifiers.
CREATE TABLE IF NOT EXISTS hid_history (
    guid        uuid NOT NULL,
    hid         text NOT NULL,
    commit_hash text NOT NULL,
    changed_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (guid, hid, commit_hash)
);

-- Deleted artifacts retained for history inspection.
CREATE TABLE IF NOT EXISTS deleted_artifacts (
    guid              uuid PRIMARY KEY,
    kind              text NOT NULL DEFAULT '',
    type              text NOT NULL DEFAULT '',
    title             text NOT NULL DEFAULT '',
    hid               text NOT NULL DEFAULT '',
    last_path         text NOT NULL,
    deleted_in_commit text NOT NULL
);

-- Repository folders (derived from stored file paths).
CREATE TABLE IF NOT EXISTS folders (
    path   text  PRIMARY KEY,
    ltpath ltree NOT NULL
);
CREATE INDEX IF NOT EXISTS folders_ltpath_gist ON folders USING gist (ltpath);

-- Repository configuration objects (schemas, workflow definitions, ...)
-- stored inside .origoa configuration folders.
CREATE TABLE IF NOT EXISTS config_objects (
    storage_path text  PRIMARY KEY,
    scope_path   text  NOT NULL,      -- folder owning the .origoa directory
    scope_lt     ltree NOT NULL,
    category     text  NOT NULL,      -- schemas | workflows | ...
    name         text  NOT NULL,
    content      text  NOT NULL
);
CREATE INDEX IF NOT EXISTS config_objects_cat_scope ON config_objects (category, scope_path);
