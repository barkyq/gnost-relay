package main

import (
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nbd-wtf/go-nostr"
	"os"
)

func StoreEvent(ctx context.Context, dbpool *pgxpool.Pool, event nostr.Event) error {
	b, err := json.Marshal(event)
	if err != nil {
		return err
	}
	ptags := make([]string, 0)
	etags := make([]string, 0)
	for _, tag := range event.Tags {
		switch {
		case tag[0] == "e":
			etags = append(etags, tag[1])
		case tag[0] == "p":
			ptags = append(ptags, tag[1])
		}
	}
	_, e := dbpool.Exec(ctx, `INSERT INTO db1 (id, pubkey, created_at, kind, ptags, etags, raw)
        VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (id) DO NOTHING;`, event.ID, event.PubKey, event.CreatedAt.Unix(), event.Kind, ptags, etags, b)
	if e != nil {
		return e
	}
	return nil
}

func InitStorage() (*pgxpool.Pool, error) {
	dbpool, err := pgxpool.New(context.Background(), os.Getenv("DATABASE_URL"))
	if err != nil {
		return nil, err
	}
	_, err = dbpool.Exec(context.Background(), `
CREATE TABLE IF NOT EXISTS db1 (
  id text NOT NULL,
  pubkey text NOT NULL,
  created_at integer NOT NULL,
  kind integer NOT NULL,
  etags text[],
  ptags text[],
  raw json
);

CREATE UNIQUE INDEX IF NOT EXISTS db1_ididx ON db1 USING btree (id text_pattern_ops);
CREATE INDEX IF NOT EXISTS db1_pubkeyprefix ON db1 USING btree (pubkey text_pattern_ops);
CREATE INDEX IF NOT EXISTS db1_timeidx ON db1 (created_at DESC);
CREATE INDEX IF NOT EXISTS db1_kindidx ON db1 (kind);
CREATE INDEX IF NOT EXISTS db1_ptagsidx ON db1 USING gin (etags);
CREATE INDEX IF NOT EXISTS db1_etagsidx ON db1 USING gin (ptags);

CREATE OR REPLACE FUNCTION notify_submission() RETURNS trigger AS $$
BEGIN
  PERFORM pg_notify('submissions',row_to_json(NEW)::text);
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS submission_notify ON db1;

CREATE TRIGGER submission_notify AFTER INSERT ON db1 FOR EACH ROW EXECUTE FUNCTION notify_submission();
`)
	return dbpool, err
}
