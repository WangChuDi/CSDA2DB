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
    *   `-force` 按 `demo_id` 替换结果，空表也会清理；全部表与处理标记在一个事务中提交，失败完整回滚。
*   **复用分析结果**：`-csv-dir` 直接导入 fork 生成的完整 37 表 CSV，无需再解析 demo。
*   每次调用只标记一个 demo；批处理需为每份文件分配独立 ID，目录或列表输入必须只解析到一份 demo。

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
go build -o csda2db.exe .
```

## 使用方法

### 基本用法

处理单个 Demo 文件：

```powershell
.\csda2db.exe -demo-id 1 -input "C:\Demos\match1.dem"
```

处理整个文件夹（递归扫描）：

```powershell
.\csda2db.exe -demo-id 1 -input "C:\Demos\Tournament2024"
```

使用 Demo 列表文件（CSV格式，每行一个路径）：

```powershell
.\csda2db.exe -demo-id 1 -input "C:\lists\to_process.csv"
```

### 进阶选项

**指定 CSDA 路径**
如果 `csda.exe` 不在当前目录或系统 PATH 中：

```powershell
.\csda2db.exe -demo-id 1 -input "..." -csda "C:\Tools\csda.exe"
```

**强制重新分析**
如果某个 Demo 已经分析过但需要更新覆盖：

```powershell
.\csda2db.exe -demo-id 1 -input "..." -force
```

## 命令行参数

已有导出的用法：`csda2db -demo-id 1 -input match.dem -csv-dir output/1 -force`，此时原始 `.dem` 可以不在磁盘。缺表、CSV 格式错误或跨校验值的记录会拒绝导入。处理标记使用 `(demo_id, filename)`，同名文件不会覆盖其他 demo；错误退出码非零。

| 参数 | 说明 | 默认值 |
| :--- | :--- | :--- |
| `-input` | **(必填)** Demo文件路径、文件夹路径或包含路径的CSV文件 | - |
| `-demo-id` | **(必填)** 本次 demo 的数据库 ID | - |
| `-csda` | CSDA 可执行文件路径 | `./csda.exe` (或系统PATH查找) |
| `-force` | 强制重新分析并覆盖数据库中的旧数据 | `false` |
| `-csv-dir` | 选中 demo 的完整已有 CSV 导出目录 | - |

## 常见问题

*   **数据库不存在？** 程序会在首次运行时自动尝试连接 `postgres` 默认库来创建目标数据库。
*   **CSDA 报错？** 请确保指定的 `-csda` 路径正确，且该版本支持 `-demo-path` 和 `-output` 参数。
