# CSDA2DB

[中文文档](README_CN.md)

CSDA2DB is a tool that automatically imports Counter-Strike 2 Demo analysis data into a PostgreSQL database. It generates CSV data by calling the [CSDA](https://github.com/akiver/cs-demo-analyzer) tool and parses/imports it, supporting automatic table structure creation and column updates.

## Features

*   **Automatic Demo Processing**: Accepts `.dem` files, directories, or a CSV list of Demos directly.
*   **Automatic Database Schema Management**:
    *   Automatically creates database tables based on CSV headers.
    *   Automatically detects new columns and executes `ALTER TABLE` to add fields.
*   **Incremental and Overwrite Updates**:
    *   Automatically records analyzed Demos to avoid duplicate processing.
    *   `-force` replaces rows for the selected `demo_id`, including empty tables, in one transaction with the processing marker. Failed imports roll back all changes.
*   **Existing CSV Exports**: `-csv-dir` imports a complete 37-table fork export without parsing the demo again.
*   Each invocation tags exactly one demo. Batch callers must assign a separate ID to each file; directory/list inputs must resolve to one demo.

## Download

Download the latest version from the [Releases](https://github.com/WangChuDi/CSDA2DB/releases) page.

## Requirements

1.  **Go** (1.21+)
2.  **PostgreSQL**
3.  **CSDA CLI** (Executable `csda.exe`)

## Quick Start

### 1. Project Setup

Clone the project and download dependencies:

```bash
git clone https://github.com/WangChuDi/CSDA2DB.git
cd CSDA2DB
go mod tidy
```

### 2. Database Configuration

Create a `.env` file in the project root directory (you can copy from `.env.example`) and fill in your database connection information:

```ini
DB_HOST=localhost
DB_PORT=5432
DB_USER=postgres
DB_PASSWORD=your_password
DB_NAME=csda2db
# DATA_DIR optional, for testing
```

### 3. Build

```bash
go build -o csda2db.exe .
```

## Usage

### Basic Usage

Process a single Demo file:

```powershell
.\csda2db.exe -demo-id 1 -input "C:\Demos\match1.dem"
```

Process an entire folder (recursive scan):

```powershell
.\csda2db.exe -demo-id 1 -input "C:\Demos\Tournament2024"
```

Use a Demo list file (CSV format, one path per line):

```powershell
.\csda2db.exe -demo-id 1 -input "C:\lists\to_process.csv"
```

### Advanced Options

**Specify CSDA Path**
If `csda.exe` is not in the current directory or system PATH:

```powershell
.\csda2db.exe -demo-id 1 -input "..." -csda "C:\Tools\csda.exe"
```

**Force Re-analysis**
If a Demo has already been analyzed but needs to be updated/overwritten:

```powershell
.\csda2db.exe -demo-id 1 -input "..." -force
```

## Command Line Arguments

For existing exports: `csda2db -demo-id 1 -input match.dem -csv-dir output/1 -force`. The original `.dem` need not be present. A missing table, malformed CSV or mismatching row checksum rejects the import. Processing records use `(demo_id, filename)` so recurring basenames do not overwrite other demos. Errors exit nonzero.

| Argument | Description | Default |
| :--- | :--- | :--- |
| `-input` | **(Required)** Demo file path, folder path, or CSV file containing paths | - |
| `-demo-id` | **(Required)** Demo identifier to tag every ingested row | - |
| `-csda` | CSDA executable path | `./csda.exe` (or searched in system PATH) |
| `-force` | Force re-analysis and overwrite old data in the database | `false` |
| `-csv-dir` | Complete existing CSV export directory for the selected demo | - |

## FAQ

*   **Database does not exist?** The program will automatically attempt to connect to the `postgres` default database to create the target database on the first run.
*   **CSDA Error?** Please ensure the specified `-csda` path is correct and that the version supports `-demo-path` and `-output` arguments.
