package main

import (
	"database/sql"
	"encoding/csv"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func integrationDB(t *testing.T) {
	t.Helper()
	if os.Getenv("CSDA2DB_TEST_DB") != "1" {
		t.Skip("set CSDA2DB_TEST_DB=1 to test an isolated PostgreSQL schema")
	}
	u := url.URL{Scheme: "postgres", Host: net.JoinHostPort(getEnv("DB_HOST", "localhost"), getEnv("DB_PORT", "5432")), Path: getEnv("DB_NAME", "cs2_pipeline"), User: url.UserPassword(getEnv("DB_USER", "postgres"), getEnv("DB_PASSWORD", ""))}
	q := u.Query()
	q.Set("sslmode", "disable")
	u.RawQuery = q.Encode()
	conn, err := sql.Open("postgres", u.String())
	if err != nil {
		t.Fatal(err)
	}
	conn.SetMaxOpenConns(1)
	schema := fmt.Sprintf("csda2db_test_%d", time.Now().UnixNano())
	if _, err := conn.Exec("CREATE SCHEMA " + schema); err != nil {
		conn.Close()
		t.Fatal(err)
	}
	if _, err := conn.Exec("SET search_path TO " + schema); err != nil {
		t.Fatal(err)
	}
	previousDB, previousForce, previousID, previousCSV := db, *force, *demoID, *csvDir
	db, *force = conn, true
	t.Cleanup(func() {
		conn.Exec("DROP SCHEMA " + schema + " CASCADE")
		conn.Close()
		db, *force, *demoID, *csvDir = previousDB, previousForce, previousID, previousCSV
	})
	// Exercise migration of the original filename-only processing key.
	if _, err := db.Exec("CREATE TABLE processed_demos (filename TEXT PRIMARY KEY, demo_id BIGINT NOT NULL, analyzed_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP, filepath TEXT)"); err != nil {
		t.Fatal(err)
	}
	if err := ensureSystemTables(); err != nil {
		t.Fatal(err)
	}
}

func exportFixture(t *testing.T, value string) string {
	t.Helper()
	dir := t.TempDir()
	for table := range managedTables {
		f, err := os.Create(filepath.Join(dir, "same-demo_"+table+".csv"))
		if err != nil {
			t.Fatal(err)
		}
		w := csv.NewWriter(f)
		header := []string{"match checksum", "value"}
		if table == "match" {
			header[0] = "checksum"
		}
		w.Write(header)
		if table == "match" || table == "damages" || table == "footsteps" {
			w.Write([]string{"shared-checksum", value})
		}
		w.Flush()
		if err := w.Error(); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
	return dir
}

func valueFor(t *testing.T, table string, id int64) string {
	t.Helper()
	var value string
	if err := db.QueryRow("SELECT value FROM \""+table+"\" WHERE demo_id=$1", id).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestAtomicReplacementScopesIdentityAndClearsEmptyTables(t *testing.T) {
	integrationDB(t)
	*csvDir = exportFixture(t, "old")
	*demoID = 10
	if err := processDemo("/first/same-demo.dem", ""); err != nil {
		t.Fatal(err)
	}
	*demoID = 20
	if err := processDemo("/second/same-demo.dem", ""); err != nil {
		t.Fatal(err)
	}
	*csvDir = exportFixture(t, "new")
	*demoID = 10
	if err := os.WriteFile(filepath.Join(*csvDir, "same-demo_footsteps.csv"), []byte("match checksum,value\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := processDemo("/first/same-demo.dem", ""); err != nil {
		t.Fatal(err)
	}
	if valueFor(t, "damages", 10) != "new" || valueFor(t, "damages", 20) != "old" || valueFor(t, "footsteps", 20) != "old" {
		t.Fatal("replacement leaked across demo IDs")
	}
	var count int
	if err := db.QueryRow("SELECT count(*) FROM footsteps WHERE demo_id=10").Scan(&count); err != nil || count != 0 {
		t.Fatalf("stale empty table: %d / %v", count, err)
	}
	if err := db.QueryRow("SELECT count(*) FROM processed_demos WHERE filename='same-demo.dem'").Scan(&count); err != nil || count != 2 {
		t.Fatalf("filename collision: %d / %v", count, err)
	}
	// A missing legacy marker must not append duplicates on retry either.
	db.Exec("DELETE FROM processed_demos WHERE demo_id=10")
	*force = false
	if err := processDemo("/first/same-demo.dem", ""); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT count(*) FROM damages WHERE demo_id=10").Scan(&count); err != nil || count != 1 {
		t.Fatal("retry appended duplicate damage")
	}
}

func TestMalformedLateCSVRestoresAllDataSchemaAndMarker(t *testing.T) {
	integrationDB(t)
	*demoID = 10
	*csvDir = exportFixture(t, "old")
	if err := processDemo("same-demo.dem", ""); err != nil {
		t.Fatal(err)
	}
	var before time.Time
	db.QueryRow("SELECT analyzed_at FROM processed_demos WHERE demo_id=10").Scan(&before)
	*csvDir = exportFixture(t, "new")
	os.WriteFile(filepath.Join(*csvDir, "same-demo_damages.csv"), []byte("match checksum,value,new column\nshared-checksum,new,added\n"), 0600)
	os.WriteFile(filepath.Join(*csvDir, "same-demo_shots.csv"), []byte("match checksum,value\nwrong-checksum,bad\n"), 0600)
	if err := processDemo("same-demo.dem", ""); err == nil || !strings.Contains(err.Error(), "another checksum") {
		t.Fatalf("expected checksum failure, got %v", err)
	}
	if valueFor(t, "damages", 10) != "old" || valueFor(t, "match", 10) != "old" {
		t.Fatal("partial import survived rollback")
	}
	var count int
	db.QueryRow("SELECT count(*) FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='damages' AND column_name='new_column'").Scan(&count)
	if count != 0 {
		t.Fatal("schema change survived rollback")
	}
	var after time.Time
	db.QueryRow("SELECT analyzed_at FROM processed_demos WHERE demo_id=10").Scan(&after)
	if !before.Equal(after) {
		t.Fatal("failed import marked completed")
	}
}

func TestIncompleteExportsRejectedBeforeReplacement(t *testing.T) {
	integrationDB(t)
	*demoID = 10
	*csvDir = exportFixture(t, "old")
	if err := processDemo("same-demo.dem", ""); err != nil {
		t.Fatal(err)
	}
	*csvDir = exportFixture(t, "new")
	os.Remove(filepath.Join(*csvDir, "same-demo_footsteps.csv"))
	if err := processDemo("same-demo.dem", ""); err == nil {
		t.Fatal("accepted incomplete export")
	}
	if valueFor(t, "match", 10) != "old" {
		t.Fatal("incomplete import changed existing data")
	}
}
