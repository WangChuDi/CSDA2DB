# CSDA2DB

[English](README.md)

CSDA2DB 是一个将 Counter-Strike 2 Demo 分析数据自动导入 PostgreSQL 数据库的工具。它通过调用 [CSDA](https://github.com/akiver/cs-demo-analyzer) 工具生成 CSV 数据，并将其解析入库，支持自动表结构创建和列更新。

## 功能特性

*   **自动处理 Demo**: 直接接受 `.dem` 文件、文件夹或 Demo 列表 CSV。
*   **自动数据库 Schema 管理**:
    *   自动根据 CSV 表头创建数据库表。
    *   自动检测新列并执行 `ALTER TABLE` 添加字段。
*   **增量与覆盖更新**:
    *   自动记录已分析过的 Demo，避免重复处理。
    *   提供 `-force` 模式，可删除指定 Demo 的旧数据并重新导入（基于 `match checksum` 或 `checksum`）。
*   **批量处理**: 支持递归扫描文件夹中的所有 Demo 文件。

## 下载

请从 [Releases](https://github.com/WangChuDi/CSDA2DB/releases) 页面下载最新版本。

## 环境要求

1.  **Go** (1.21+)
2.  **PostgreSQL**
3.  **CSDA CLI** (可执行文件 `csda.exe`)

## 快速开始

### 1. 配置项目

克隆项目并下载依赖：

```bash
git clone https://github.com/WangChuDi/CSDA2DB.git
cd CSDA2DB
go mod tidy
```

### 2. 数据库配置

在项目根目录创建一个 `.env` 文件（可以复制 `.env.example`），填入你的数据库连接信息：

```ini
DB_HOST=localhost
DB_PORT=5432
DB_USER=postgres
DB_PASSWORD=your_password
DB_NAME=csda2db
# DATA_DIR 可选，用于测试
```

### 3. 编译

```bash
go build -o csda2db.exe main.go
```

## 使用方法

### 基本用法

处理单个 Demo 文件：

```powershell
.\csda2db.exe -input "C:\Demos\match1.dem"
```

处理整个文件夹（递归扫描）：

```powershell
.\csda2db.exe -input "C:\Demos\Tournament2024"
```

使用 Demo 列表文件（CSV格式，每行一个路径）：

```powershell
.\csda2db.exe -input "C:\lists\to_process.csv"
```

### 进阶选项

**指定 CSDA 路径**
如果 `csda.exe` 不在当前目录或系统 PATH 中：

```powershell
.\csda2db.exe -input "..." -csda "C:\Tools\csda.exe"
```

**强制重新分析**
如果某个 Demo 已经分析过但需要更新覆盖：

```powershell
.\csda2db.exe -input "..." -force
```

## 命令行参数

| 参数 | 说明 | 默认值 |
| :--- | :--- | :--- |
| `-input` | **(必填)** Demo文件路径、文件夹路径或包含路径的CSV文件 | - |
| `-csda` | CSDA 可执行文件路径 | `./csda.exe` (或系统PATH查找) |
| `-force` | 强制重新分析并覆盖数据库中的旧数据 | `false` |

## 常见问题

*   **数据库不存在？** 程序会在首次运行时自动尝试连接 `postgres` 默认库来创建目标数据库。
*   **CSDA 报错？** 请确保指定的 `-csda` 路径正确，且该版本支持 `-demo-path` 和 `-output` 参数。
