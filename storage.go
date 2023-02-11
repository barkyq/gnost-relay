package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"

	"os"
)

func (ev *EventSubmission) StoreEvent(dbconn *pgxpool.Conn, string_buf_pool *sync.Pool) error {
	b, err := json.Marshal(ev.event)
	if err != nil {
		return err
	}
	ptags := string_buf_pool.Get().([]string)
	etags := string_buf_pool.Get().([]string)
	gtags := string_buf_pool.Get().([]string)
	defer string_buf_pool.Put(ptags)
	defer string_buf_pool.Put(etags)
	defer string_buf_pool.Put(gtags)

	var dtag *string
	var expiration *int64
	for _, tag := range ev.event.Tags {
		switch {
		case tag[0] == "e":
			if b, e := hex.DecodeString(tag[1]); e != nil || len(b) != 32 {
				continue
			} else {
				etags = append(etags, fmt.Sprintf("%x", b))
			}
		case tag[0] == "p":
			if b, e := hex.DecodeString(tag[1]); e != nil || len(b) != 32 {
				continue
			} else {
				ptags = append(ptags, fmt.Sprintf("%x", b))
			}
		case tag[0] == "d":
			dtag = &tag[1]
		case tag[0] == "expiration":
			if t, e := strconv.ParseInt(tag[1], 10, 64); e == nil {
				expiration = &t
			}
		case len(tag[0]) == 1 && len(tag) > 0:
			gtags = append(gtags, "#"+tag[0]+":"+tag[1])
		}
	}
	_, e := dbconn.Exec(ev.ctx, `INSERT INTO db1 (id, pubkey, created_at, kind, ptags, etags, dtag, expiration, gtags, raw)
        VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (id) DO NOTHING;`, ev.event.ID, ev.event.PubKey, ev.event.CreatedAt.Unix(), ev.event.Kind, ptags, etags, dtag, expiration, gtags, b)
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
  dtag text,
  expiration integer,
  gtags text[],
  raw json
);

CREATE UNIQUE INDEX IF NOT EXISTS db1_ididx ON db1 USING btree (id text_pattern_ops);
CREATE INDEX IF NOT EXISTS db1_pubkeyprefix ON db1 USING btree (pubkey text_pattern_ops);
CREATE INDEX IF NOT EXISTS db1_timeidx ON db1 (created_at DESC);
CREATE INDEX IF NOT EXISTS db1_kindidx ON db1 (kind);
CREATE INDEX IF NOT EXISTS db1_ptagsidx ON db1 USING gin (etags);
CREATE INDEX IF NOT EXISTS db1_etagsidx ON db1 USING gin (ptags);
CREATE INDEX IF NOT EXISTS db1_gtagsidx ON db1 USING gin (gtags);
CREATE INDEX IF NOT EXISTS db1_expireidx ON db1 (expiration DESC);

CREATE OR REPLACE FUNCTION delete_submission() RETURNS trigger AS $$
BEGIN  
  IF NEW.kind=5 THEN
    PERFORM pg_notify('submissions',row_to_json(NEW)::text);
    DELETE FROM db1 WHERE ARRAY[id] && NEW.etags AND NEW.pubkey=pubkey;
    RETURN NULL;
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE FUNCTION ephemeral_submission() RETURNS trigger AS $$
DECLARE 
unixnow integer;
BEGIN
  IF int4range(20000,29999) @> NEW.kind OR (NEW.expiration is not null and NEW.expiration <= NEW.created_at) THEN
    PERFORM pg_notify('submissions',row_to_json(NEW)::text);
    RETURN NULL;
  END IF;
  IF (NEW.expiration is null) THEN
    RETURN NEW;
  END IF;
  SELECT extract(epoch from now())::integer INTO unixnow;
  IF NEW.expiration <= unixnow THEN
    RETURN NULL;
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE FUNCTION param_replaceable_submission() RETURNS trigger AS $$
DECLARE 
ca integer;
BEGIN
  IF NEW.dtag is not null OR int4range(30000,39999) @> NEW.kind THEN
    SELECT created_at INTO ca FROM db1 WHERE kind=NEW.kind AND dtag=NEW.dtag AND pubkey=NEW.pubkey ORDER BY created_at DESC;
    IF NOT FOUND OR NEW.created_at > ca THEN
      DELETE FROM db1 WHERE kind=NEW.kind AND pubkey=NEW.pubkey AND dtag=NEW.dtag AND created_at <= NEW.created_at;
    ELSE
      RETURN NULL;
    END IF;
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE FUNCTION replaceable_submission() RETURNS trigger AS $$
DECLARE 
ca integer;
BEGIN  
  IF int4range(10000,19999) @> NEW.kind OR NEW.kind in (0,2,3,41) THEN
    SELECT created_at INTO ca FROM db1 WHERE kind=NEW.kind AND pubkey=NEW.pubkey;
    IF NOT FOUND OR NEW.created_at > ca THEN
      DELETE FROM db1 WHERE kind=NEW.kind AND pubkey=NEW.pubkey AND created_at <= NEW.created_at;
    ELSE
      RETURN NULL;
    END IF;
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE FUNCTION notify_submission() RETURNS trigger AS $$
BEGIN
  PERFORM pg_notify('submissions',row_to_json(NEW)::text);
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS delete_trigger ON db1;
DROP TRIGGER IF EXISTS ephemeral_trigger ON db1;
DROP TRIGGER IF EXISTS param_replaceable_trigger ON db1;
DROP TRIGGER IF EXISTS replaceable_trigger ON db1;
DROP TRIGGER IF EXISTS submission_trigger ON db1;

CREATE TRIGGER delete_trigger BEFORE INSERT ON db1 FOR EACH ROW EXECUTE FUNCTION delete_submission();
CREATE TRIGGER ephemeral_trigger BEFORE INSERT ON db1 FOR EACH ROW EXECUTE FUNCTION ephemeral_submission();
CREATE TRIGGER param_replaceable_trigger BEFORE INSERT ON db1 FOR EACH ROW EXECUTE FUNCTION param_replaceable_submission();
CREATE TRIGGER replaceable_trigger BEFORE INSERT ON db1 FOR EACH ROW EXECUTE FUNCTION replaceable_submission();
CREATE TRIGGER submission_trigger AFTER INSERT ON db1 FOR EACH ROW EXECUTE FUNCTION notify_submission();
`)
	return dbpool, err
}
