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
	resolvedCsdaPath, err := exec.LookPath(*csdaPath)
	if err != nil {
		log.Fatalf("CSDA executable not found (%s): %v", *csdaPath, err)
	}
	resolvedCsdaPath, err = filepath.Abs(resolvedCsdaPath)
	if err != nil {
		log.Fatalf("Failed to resolve absolute path of CSDA executable: %v", err)
	}
	log.Printf("Using CSDA executable at: %s", resolvedCsdaPath)

	// 4. Get list of demos
	demos, err := getDemoList(*inputPath)
	if err != nil {
		log.Fatalf("Failed to get demo list: %v", err)
	}
	log.Printf("Found %d demos to process", len(demos))

	// 5. Process each demo
	for _, demoPath := range demos {
		if err := processDemo(demoPath, resolvedCsdaPath); err != nil {
			log.Printf("Error processing demo %s: %v", demoPath, err)
		}
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
	_, err := db.Exec(alterQuery)
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
		err := db.QueryRow("SELECT EXISTS(SELECT 1 FROM processed_demos WHERE filename = $1)", filename).Scan(&exists)
		if err != nil {
			return err
		}
		if exists {
			log.Printf("Skipping demo %s (already analyzed). Use -force to re-analyze.", filename)
			return nil
		}
	}

	log.Printf("Analyzing demo: %s", filename)

	tempDir, err := os.MkdirTemp("", "csda_output_*")
	if err != nil {
		return fmt.Errorf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	log.Printf("Running CSDA: %s -demo-path %s -output %s", csdaExecPath, demoPath, tempDir)
	cmd := exec.Command(csdaExecPath, "-demo-path", demoPath, "-output", tempDir, "-source", "valve")

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("csda executation failed: %v, output: %s", err, string(output))
	}

	files, err := filepath.Glob(filepath.Join(tempDir, "*.csv"))
	if err != nil {
		return fmt.Errorf("failed to list generated CSVs: %v", err)
	}

	if len(files) == 0 {
		return fmt.Errorf("no CSV files generated for demo %s", filename)
	}

	for _, file := range files {
		if err := processCSVFile(file, demoBaseName); err != nil {
			log.Printf("Error processing CSV %s: %v", filepath.Base(file), err)
			return err
		}
	}

	// 2. Mark as processed
	_, err = db.Exec(`
		INSERT INTO processed_demos (filename, filepath, analyzed_at, demo_id)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (filename) DO UPDATE SET
			analyzed_at = EXCLUDED.analyzed_at,
			filepath = EXCLUDED.filepath,
			demo_id = EXCLUDED.demo_id;
	`, filename, demoPath, time.Now(), *demoID)

	if err != nil {
		return fmt.Errorf("failed to update processed_demos status: %v", err)
	}

	log.Printf("Successfully analyzed and stored %s", filename)
	return nil
}

func processCSVFile(filePath string, demoBaseName string) error {
	fileName := filepath.Base(filePath)

	prefix := demoBaseName + "_"
	if !strings.HasPrefix(fileName, prefix) {
		return fmt.Errorf("filename %s does not start with expected prefix %s", fileName, prefix)
	}

	tableNameWithExt := strings.TrimPrefix(fileName, prefix)
	tableName := strings.TrimSuffix(tableNameWithExt, ".csv")
	tableName = strings.ToLower(tableName)

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

	if err := ensureSchema(tableName, headers); err != nil {
		return fmt.Errorf("failed to ensure schema for table %s: %v", tableName, err)
	}

	// Read first record to get identifier for potential cleanup
	firstRecord, err := reader.Read()
	if err != nil && err != io.EOF {
		return fmt.Errorf("failed to read first record: %v", err)
	}
	if err == io.EOF {
		log.Printf("File %s is empty, skipping", fileName)
		return nil
	}

	// Handle cleanup if force is enabled
	if *force {
		// Try to find "match checksum" OR "checksum" column
		checksumIdx := -1
		colName := ""
		for i, h := range headers {
			if h == "match_checksum" {
				checksumIdx = i
				colName = h
				break
			}
			if h == "checksum" {
				checksumIdx = i
				colName = h
			}
		}

		if checksumIdx != -1 && len(firstRecord) > checksumIdx {
			checksum := firstRecord[checksumIdx]
			log.Printf("Force enabled: Cleaning up existing data for %s '%s' in table '%s'", colName, checksum, tableName)
			// Use the actual column name found in the header to be safe, properly quoted
			_, err := db.Exec(fmt.Sprintf("DELETE FROM \"%s\" WHERE \"%s\" = $1", tableName, colName), checksum)
			if err != nil {
				return fmt.Errorf("failed to delete existing data: %v", err)
			}
		} else {
			log.Printf("Warning: Force enabled but 'match checksum' or 'checksum' column not found in %s. Appending data without cleanup.", fileName)
		}
	}

	txn, err := db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %v", err)
	}
	defer txn.Rollback()

	copyCols := append([]string{"demo_id"}, headers...)
	stmt, err := txn.Prepare(pq.CopyIn(tableName, copyCols...))
	if err != nil {
		return fmt.Errorf("failed to prepare COPY statement: %v", err)
	}
	defer stmt.Close()

	// Helper to insert a record
	demoIDValue := *demoID
	insertRecord := func(rec []string, line int) error {
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

	if err = txn.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %v", err)
	}

	return nil
}

func ensureSchema(tableName string, headers []string) error {
	var exists bool
	checkTableQuery := fmt.Sprintf("SELECT EXISTS (SELECT FROM information_schema.tables WHERE table_schema = 'public' AND table_name = '%s')", tableName)
	err := db.QueryRow(checkTableQuery).Scan(&exists)
	if err != nil {
		return err
	}

	if !exists {
		return createTable(tableName, headers)
	}
	return syncColumns(tableName, headers)
}

func createTable(tableName string, headers []string) error {
	cols := []string{"\"demo_id\" BIGINT NOT NULL"}
	for _, h := range headers {
		cols = append(cols, fmt.Sprintf("\"%s\" TEXT", h))
	}
	query := fmt.Sprintf("CREATE TABLE \"%s\" (%s)", tableName, strings.Join(cols, ", "))
	log.Printf("Creating table %s", tableName)
	_, err := db.Exec(query)
	return err
}

func syncColumns(tableName string, headers []string) error {
	rows, err := db.Query("SELECT column_name FROM information_schema.columns WHERE table_schema = 'public' AND table_name = $1", tableName)
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

	if !existingCols["demo_id"] {
		log.Printf("Adding missing column 'demo_id' to table '%s'", tableName)
		query := fmt.Sprintf("ALTER TABLE \"%s\" ADD COLUMN IF NOT EXISTS \"demo_id\" BIGINT NOT NULL DEFAULT 0", tableName)
		if _, err := db.Exec(query); err != nil {
			return err
		}
		existingCols["demo_id"] = true
	}

	for _, h := range headers {
		if !existingCols[h] {
			log.Printf("Adding missing column '%s' to table '%s'", h, tableName)
			query := fmt.Sprintf("ALTER TABLE \"%s\" ADD COLUMN \"%s\" TEXT", tableName, h)
			if _, err := db.Exec(query); err != nil {
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
