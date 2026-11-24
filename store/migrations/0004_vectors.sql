-- Phase 2, last step (M-10): semantic search. Exact scan, no HNSW index yet:
-- with filters (forgotten_at, dates) an approximate index can return fewer than
-- k rows, and tens of thousands of 1024-dim rows scan in tens of milliseconds.
-- Install in public explicitly: tests run with search_path=<throwaway>,public.
CREATE EXTENSION IF NOT EXISTS vector SCHEMA public;

ALTER TABLE messages ADD COLUMN embedding vector(1024), ADD COLUMN embedding_model text;
ALTER TABLE facts    ADD COLUMN embedding vector(1024), ADD COLUMN embedding_model text;
