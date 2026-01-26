-- PostgreSQL schema version 1
-- Initial schema without model or branch columns.
-- Note: Version is managed by EnsureSchema(), not this file.

CREATE SCHEMA IF NOT EXISTS roborev;

CREATE TABLE IF NOT EXISTS roborev.schema_version (
  version INTEGER PRIMARY KEY,
  applied_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS roborev.machines (
  id SERIAL PRIMARY KEY,
  machine_id UUID UNIQUE NOT NULL,
  name TEXT,
  last_seen_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS roborev.repos (
  id SERIAL PRIMARY KEY,
  identity TEXT UNIQUE NOT NULL,
  created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS roborev.commits (
  id SERIAL PRIMARY KEY,
  repo_id INTEGER REFERENCES roborev.repos(id),
  sha TEXT NOT NULL,
  author TEXT NOT NULL,
  subject TEXT NOT NULL,
  timestamp TIMESTAMP WITH TIME ZONE NOT NULL,
  created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
  UNIQUE(repo_id, sha)
);

CREATE TABLE IF NOT EXISTS roborev.review_jobs (
  id SERIAL PRIMARY KEY,
  uuid UUID UNIQUE NOT NULL,
  repo_id INTEGER NOT NULL REFERENCES roborev.repos(id),
  commit_id INTEGER REFERENCES roborev.commits(id),
  git_ref TEXT NOT NULL,
  agent TEXT NOT NULL,
  reasoning TEXT,
  status TEXT NOT NULL CHECK(status IN ('done', 'failed', 'canceled')),
  agentic BOOLEAN DEFAULT FALSE,
  enqueued_at TIMESTAMP WITH TIME ZONE NOT NULL,
  started_at TIMESTAMP WITH TIME ZONE,
  finished_at TIMESTAMP WITH TIME ZONE,
  prompt TEXT,
  diff_content TEXT,
  error TEXT,
  source_machine_id UUID NOT NULL,
  created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
  updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS roborev.reviews (
  id SERIAL PRIMARY KEY,
  uuid UUID UNIQUE NOT NULL,
  job_uuid UUID NOT NULL REFERENCES roborev.review_jobs(uuid),
  agent TEXT NOT NULL,
  prompt TEXT NOT NULL,
  output TEXT NOT NULL,
  addressed BOOLEAN NOT NULL DEFAULT FALSE,
  updated_by_machine_id UUID NOT NULL,
  created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
  updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS roborev.responses (
  id SERIAL PRIMARY KEY,
  uuid UUID UNIQUE NOT NULL,
  job_uuid UUID NOT NULL REFERENCES roborev.review_jobs(uuid),
  responder TEXT NOT NULL,
  response TEXT NOT NULL,
  source_machine_id UUID NOT NULL,
  created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_review_jobs_source ON roborev.review_jobs(source_machine_id);
CREATE INDEX IF NOT EXISTS idx_review_jobs_updated ON roborev.review_jobs(updated_at);
CREATE INDEX IF NOT EXISTS idx_reviews_job_uuid ON roborev.reviews(job_uuid);
CREATE INDEX IF NOT EXISTS idx_reviews_updated ON roborev.reviews(updated_at);
CREATE INDEX IF NOT EXISTS idx_responses_job_uuid ON roborev.responses(job_uuid);
CREATE INDEX IF NOT EXISTS idx_responses_id ON roborev.responses(id);

CREATE TABLE IF NOT EXISTS roborev.sync_metadata (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
);

INSERT INTO roborev.schema_version (version) VALUES (1);
