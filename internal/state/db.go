package state

import (
	"context"
	"database/sql"

	_ "modernc.org/sqlite"
)

type DB struct {
	conn *sql.DB
}

func Open(ctx context.Context, path string) (*DB, error) {
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db := &DB{conn: conn}
	if err := db.configure(ctx); err != nil {
		conn.Close()
		return nil, err
	}
	if err := db.Migrate(ctx); err != nil {
		conn.Close()
		return nil, err
	}
	return db, nil
}

func (db *DB) Close() error {
	return db.conn.Close()
}

func (db *DB) Conn() *sql.DB {
	return db.conn
}

func (db *DB) configure(ctx context.Context) error {
	pragmas := []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
		"PRAGMA busy_timeout=5000",
	}
	for _, pragma := range pragmas {
		if _, err := db.conn.ExecContext(ctx, pragma); err != nil {
			return err
		}
	}
	return nil
}
