package main

import (
	"bufio"
	"database/sql"
	"encoding/csv"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"github.com/lib/pq"
)

var (
	inputPath = flag.String("input", "", "Path to a CSV file containing demo paths, or a directory to search for .dem files")
	force     = flag.Bool("force", false, "Force re-analysis of demos even if they exist in the database")
	csdaPath  = flag.String("csda", "./csda.exe", "Path to the csda executable")
	demoID    = flag.Int64("demo-id", -1, "Demo identifier to tag every ingested row (required)")
	csvDir    = flag.String("csv-dir", "", "Import existing CSDA CSV exports for one demo without parsing it again")

	// Regex to extract table name from CSV filename
	// Example: fut-vs-astralis-m2-mirage_kills.csv -> kills
	filenameDataRegex = regexp.MustCompile(`^.+_(.+)\.csv$`)
)

func main() {
	flag.Parse()

	if *inputPath == "" {
		os.Stderr.WriteString("Error: -input argument is required\n\n")
		flag.Usage()
		os.Exit(1)
	}

	if *demoID < 0 {
		os.Stderr.WriteString("Error: -demo-id argument is required\n\n")
		flag.Usage()
		os.Exit(1)
	}

	// 1. Load configuration and Connect DB
	loadConfigAndConnect()
	defer db.Close()

	// 2. Ensure system tables exist
	if err := ensureSystemTables(); err != nil {
		log.Fatalf("Failed to ensure system tables: %v", err)
	}

	// 3. Resolve CSDA path
	var resolvedCsdaPath string
	var err error
	if *csvDir == "" {
		resolvedCsdaPath, err = exec.LookPath(*csdaPath)
		if err != nil {
			log.Fatalf("CSDA executable not found: %v", err)
		}
		resolvedCsdaPath, err = filepath.Abs(resolvedCsdaPath)
		if err != nil {
			log.Fatal(err)
		}
	}

	// 4. Get list of demos
	var demos []string
	if *csvDir != "" && strings.EqualFold(filepath.Ext(*inputPath), ".dem") {
		demos = []string{*inputPath} // CSV import does not require the original file.
	} else {
		demos, err = getDemoList(*inputPath)
	}
	if err != nil {
		log.Fatalf("Failed to get demo list: %v", err)
	}
	log.Printf("Found %d demos to process", len(demos))
	if len(demos) != 1 {
		log.Fatal("one -demo-id must identify exactly one demo")
	}

	// 5. Process each demo
	failed := false
	for _, demoPath := range demos {
		if err := processDemo(demoPath, resolvedCsdaPath); err != nil {
			log.Printf("Error processing demo %s: %v", demoPath, err)
			failed = true
		}
	}
	if failed {
		os.Exit(1)
	}
}

var db *sql.DB

func loadConfigAndConnect() {
	err := godotenv.Load()
	if err != nil {
		log.Println("Warning: Error loading .env file, continuing with environment variables")
	}

	dbHost := getEnv("DB_HOST", "localhost")
	dbPort := getEnv("DB_PORT", "5432")
	dbUser := getEnv("DB_USER", "postgres")
	dbPass := getEnv("DB_PASSWORD", "password")
	dbName := getEnv("DB_NAME", "postgres")

	// 1. Connect to default 'postgres' database to check/create target DB
	// We use dbname=postgres as the maintenance DB context
	defaultConnStr := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=postgres sslmode=disable",
		dbHost, dbPort, dbUser, dbPass)

	defaultDb, err := sql.Open("postgres", defaultConnStr)
	if err != nil {
		log.Fatalf("Failed to open connection to default postgres database: %v", err)
	}

	// Check availability of default connection
	if err := defaultDb.Ping(); err != nil {
		defaultDb.Close()
		log.Fatalf("Failed to connect to default postgres database (check user/password): %v", err)
	}

	// Check if target database exists
	var exists bool
	checkDbQuery := fmt.Sprintf("SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname = '%s')", dbName)
	if err := defaultDb.QueryRow(checkDbQuery).Scan(&exists); err != nil {
		defaultDb.Close()
		log.Fatalf("Failed to check if database '%s' exists: %v", dbName, err)
	}

	if !exists {
		log.Printf("Database '%s' does not exist. Creating it...", dbName)
		createDbQuery := fmt.Sprintf("CREATE DATABASE \"%s\"", dbName)
		if _, err := defaultDb.Exec(createDbQuery); err != nil {
			defaultDb.Close()
			log.Fatalf("Failed to create database '%s': %v", dbName, err)
		}
		log.Printf("Successfully created database '%s'", dbName)
	}
	defaultDb.Close()

	// 2. Connect to the actual target database
	connStr := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=disable",
		dbHost, dbPort, dbUser, dbPass, dbName)

	db, err = sql.Open("postgres", connStr)
	if err != nil {
		log.Fatalf("Failed to open database connection: %v", err)
	}

	if err = db.Ping(); err != nil {
		log.Fatalf("Failed to ping database: %v", err)
	}
	log.Println("Successfully connected to database")
}

func ensureSystemTables() error {
	query := `
	CREATE TABLE IF NOT EXISTS processed_demos (
		filename TEXT PRIMARY KEY,
		demo_id BIGINT NOT NULL,
		analyzed_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
		filepath TEXT
	);`
	if _, err := db.Exec(query); err != nil {
		return err
	}

	alterQuery := `
	ALTER TABLE processed_demos
		ADD COLUMN IF NOT EXISTS demo_id BIGINT NOT NULL DEFAULT 0;
	`
	if _, err := db.Exec(alterQuery); err != nil {
		return err
	}
	// A basename can recur in different match directories. Keep each demo's
	// processing record instead of overwriting another demo's filename entry.
	_, err := db.Exec(`DO $$ DECLARE old_key text; BEGIN
	 SELECT conname INTO old_key FROM pg_constraint
	 WHERE conrelid='processed_demos'::regclass AND contype='p'
	 AND pg_get_constraintdef(oid)='PRIMARY KEY (filename)';
	 IF old_key IS NOT NULL THEN
	  EXECUTE format('ALTER TABLE processed_demos DROP CONSTRAINT %I', old_key);
	  ALTER TABLE processed_demos ADD PRIMARY KEY (demo_id, filename);
	 END IF;
	END $$;`)
	return err
}

func getDemoList(input string) ([]string, error) {
	var demos []string
	info, err := os.Stat(input)
	if err != nil {
		return nil, err
	}

	if info.IsDir() {
		// Walk directory
		err = filepath.Walk(input, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if !info.IsDir() && strings.HasSuffix(strings.ToLower(info.Name()), ".dem") {
				absPath, _ := filepath.Abs(path)
				demos = append(demos, absPath)
			}
			return nil
		})
	} else {
		// Assume file is CSV list of paths or a single .dem file (though usually user said CSV or Folder)
		// Check extension
		if strings.HasSuffix(strings.ToLower(input), ".dem") {
			absPath, _ := filepath.Abs(input)
			demos = append(demos, absPath)
		} else {
			// Read CSV list
			f, err := os.Open(input)
			if err != nil {
				return nil, err
			}
			defer f.Close()
			scanner := bufio.NewScanner(f)
			for scanner.Scan() {
				line := strings.TrimSpace(scanner.Text())
				if line != "" {
					absPath, _ := filepath.Abs(line)
					demos = append(demos, absPath)
				}
			}
			if err := scanner.Err(); err != nil {
				return nil, err
			}
		}
	}
	return demos, err
}

func processDemo(demoPath, csdaExecPath string) error {
	filename := filepath.Base(demoPath)
	demoBaseName := strings.TrimSuffix(filename, filepath.Ext(filename))

	if !*force {
		var exists bool
		err := db.QueryRow("SELECT EXISTS(SELECT 1 FROM processed_demos WHERE demo_id = $1 AND filename = $2)", *demoID, filename).Scan(&exists)
		if err != nil {
			return err
		}
		if exists {
			log.Printf("Skipping demo %s (already analyzed). Use -force to re-analyze.", filename)
			return nil
		}
	}

	log.Printf("Analyzing demo: %s", filename)

	tempDir := *csvDir
	if tempDir == "" {
		var err error
		tempDir, err = os.MkdirTemp("", "csda_output_*")
		if err != nil {
			return err
		}
		defer os.RemoveAll(tempDir)
		cmd := exec.Command(csdaExecPath, "-demo-path", demoPath, "-output", tempDir, "-source", "valve")
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("csda execution failed: %v, output: %s", err, string(output))
		}
	}

	files, err := filepath.Glob(filepath.Join(tempDir, "*.csv"))
	if err != nil {
		return fmt.Errorf("failed to list generated CSVs: %v", err)
	}

	if len(files) == 0 {
		return fmt.Errorf("no CSV files generated for demo %s", filename)
	}

	checksum, err := validateExportSet(files, demoBaseName)
	if err != nil {
		return err
	}
	txn, err := db.Begin()
	if err != nil {
		return err
	}
	defer txn.Rollback()
	// Serialize imports of the same demo; all replacement and the marker commit
	// together. A failed CSV also rolls back earlier deletes, inserts and DDL.
	if _, err := txn.Exec("SELECT pg_advisory_xact_lock($1)", *demoID); err != nil {
		return err
	}
	for _, file := range files {
		if err := processCSVFile(txn, file, demoBaseName, checksum); err != nil {
			log.Printf("Error processing CSV %s: %v", filepath.Base(file), err)
			return err
		}
	}

	// 2. Mark as processed
	_, err = txn.Exec(`
		INSERT INTO processed_demos (filename, filepath, analyzed_at, demo_id)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (demo_id, filename) DO UPDATE SET
			analyzed_at = EXCLUDED.analyzed_at,
			filepath = EXCLUDED.filepath,
			demo_id = EXCLUDED.demo_id;
	`, filename, demoPath, time.Now(), *demoID)

	if err != nil {
		return fmt.Errorf("failed to update processed_demos status: %v", err)
	}
	if err := txn.Commit(); err != nil {
		return err
	}

	log.Printf("Successfully analyzed and stored %s", filename)
	return nil
}

func processCSVFile(txn *sql.Tx, filePath string, demoBaseName string, checksum string) error {
	fileName := filepath.Base(filePath)

	prefix := demoBaseName + "_"
	if !strings.HasPrefix(fileName, prefix) {
		return fmt.Errorf("filename %s does not start with expected prefix %s", fileName, prefix)
	}

	tableNameWithExt := strings.TrimPrefix(fileName, prefix)
	tableName := strings.TrimSuffix(tableNameWithExt, ".csv")
	tableName = strings.ToLower(tableName)
	if !managedTables[tableName] {
		return fmt.Errorf("unknown CSDA table %q", tableName)
	}

	f, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer f.Close()

	reader := csv.NewReader(f)
	headers, err := reader.Read()
	if err != nil {
		return fmt.Errorf("failed to read header: %v", err)
	}
	// Normalize column names
	for i, h := range headers {
		headers[i] = normalizeColumnName(h)
	}
	if err := validateHeaders(headers); err != nil {
		return err
	}

	if err := ensureSchema(txn, tableName, headers); err != nil {
		return fmt.Errorf("failed to ensure schema for table %s: %v", tableName, err)
	}

	// Delete by identity, including header-only exports, inside the demo transaction.
	if _, err := txn.Exec("DELETE FROM "+pq.QuoteIdentifier(tableName)+" WHERE demo_id=$1", *demoID); err != nil {
		return err
	}
	// Read first record, after clearing stale rows from empty tables too.
	firstRecord, err := reader.Read()
	if err != nil && err != io.EOF {
		return fmt.Errorf("failed to read first record: %v", err)
	}
	if err == io.EOF {
		log.Printf("File %s is empty, skipping", fileName)
		return nil
	}

	copyCols := append([]string{"demo_id"}, headers...)
	stmt, err := txn.Prepare(pq.CopyIn(tableName, copyCols...))
	if err != nil {
		return fmt.Errorf("failed to prepare COPY statement: %v", err)
	}
	defer stmt.Close()

	// Helper to insert a record
	demoIDValue := *demoID
	insertRecord := func(rec []string, line int) error {
		for i, header := range headers {
			if (header == "checksum" || header == "match_checksum") && rec[i] != checksum {
				return fmt.Errorf("row %d belongs to another checksum", line)
			}
		}
		args := make([]interface{}, len(rec)+1)
		args[0] = demoIDValue
		for i, v := range rec {
			args[i+1] = v
		}
		if _, err := stmt.Exec(args...); err != nil {
			return fmt.Errorf("error executing COPY for row %d: %v", line, err)
		}
		return nil
	}

	// Write the first record we already read
	if err := insertRecord(firstRecord, 1); err != nil {
		return err
	}

	// Write remaining records
	rowLine := 2
	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("error reading row %d: %v", rowLine, err)
		}
		if err := insertRecord(record, rowLine); err != nil {
			return err
		}
		rowLine++
	}

	if _, err = stmt.Exec(); err != nil {
		return fmt.Errorf("error flushing COPY: %v", err)
	}

	return nil
}

func ensureSchema(txn *sql.Tx, tableName string, headers []string) error {
	var exists bool
	err := txn.QueryRow("SELECT EXISTS (SELECT FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = $1)", tableName).Scan(&exists)
	if err != nil {
		return err
	}

	if !exists {
		return createTable(txn, tableName, headers)
	}
	return syncColumns(txn, tableName, headers)
}

func createTable(txn *sql.Tx, tableName string, headers []string) error {
	cols := []string{"\"demo_id\" BIGINT NOT NULL"}
	for _, h := range headers {
		cols = append(cols, pq.QuoteIdentifier(h)+" TEXT")
	}
	query := fmt.Sprintf("CREATE TABLE IF NOT EXISTS \"%s\" (%s)", tableName, strings.Join(cols, ", "))
	log.Printf("Creating table %s", tableName)
	_, err := txn.Exec(query)
	return err
}

func syncColumns(txn *sql.Tx, tableName string, headers []string) error {
	rows, err := txn.Query("SELECT column_name FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = $1", tableName)
	if err != nil {
		return err
	}
	defer rows.Close()

	existingCols := make(map[string]bool)
	for rows.Next() {
		var colName string
		if err := rows.Scan(&colName); err != nil {
			return err
		}
		existingCols[colName] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()

	if !existingCols["demo_id"] {
		log.Printf("Adding missing column 'demo_id' to table '%s'", tableName)
		query := fmt.Sprintf("ALTER TABLE \"%s\" ADD COLUMN IF NOT EXISTS \"demo_id\" BIGINT NOT NULL DEFAULT 0", tableName)
		if _, err := txn.Exec(query); err != nil {
			return err
		}
		existingCols["demo_id"] = true
	}

	for _, h := range headers {
		if !existingCols[h] {
			log.Printf("Adding missing column '%s' to table '%s'", h, tableName)
			query := "ALTER TABLE " + pq.QuoteIdentifier(tableName) + " ADD COLUMN IF NOT EXISTS " + pq.QuoteIdentifier(h) + " TEXT"
			if _, err := txn.Exec(query); err != nil {
				return err
			}
		}
	}
	return nil
}

func getEnv(key, fallback string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return fallback
}

func normalizeColumnName(name string) string {
	return strings.ReplaceAll(name, " ", "_")
}
