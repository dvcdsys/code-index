"""
Migration script to convert from UUID-based project IDs to path-based identification.

This script:
1. Creates backup of the database
2. Creates new tables with path-based schema
3. Migrates data from old tables to new ones
4. Renames tables (old -> backup, new -> main)

Usage:
    python migrate_to_path_based.py --db-path /path/to/projects.db
"""
import argparse
import asyncio
import shutil
import sqlite3
from datetime import datetime
from pathlib import Path


async def migrate_database(db_path: str, dry_run: bool = False):
    """Migrate database from UUID-based to path-based schema."""
    db_file = Path(db_path)

    if not db_file.exists():
        print(f"❌ Database file not found: {db_path}")
        return False

    # Create backup
    backup_path = db_file.parent / f"{db_file.stem}_backup_{datetime.now().strftime('%Y%m%d_%H%M%S')}.db"
    if not dry_run:
        print(f"📦 Creating backup: {backup_path}")
        shutil.copy2(db_path, backup_path)
    else:
        print(f"[DRY RUN] Would create backup: {backup_path}")

    conn = sqlite3.connect(db_path)
    conn.row_factory = sqlite3.Row
    cursor = conn.cursor()

    try:
        # Check if migration is needed
        cursor.execute("SELECT sql FROM sqlite_master WHERE type='table' AND name='projects'")
        table_schema = cursor.fetchone()
        if table_schema and 'host_path TEXT PRIMARY KEY' in table_schema[0]:
            print("✅ Database already using path-based schema. No migration needed.")
            return True

        # Get existing projects
        cursor.execute("SELECT * FROM projects")
        projects = cursor.fetchall()

        if not projects:
            print("ℹ️  No projects found. Creating new schema...")
            if not dry_run:
                # Just rename tables and create new ones
                cursor.execute("DROP TABLE IF EXISTS projects_old")
                cursor.execute("ALTER TABLE projects RENAME TO projects_old")
                _create_new_tables(cursor)
                conn.commit()
            return True

        print(f"📊 Found {len(projects)} project(s) to migrate")

        # Create new tables with _new suffix
        if not dry_run:
            _create_new_tables_with_suffix(cursor, "_new")

        # Migrate data
        for project in projects:
            host_path = project['host_path']
            print(f"  Migrating: {host_path}")

            if dry_run:
                print(f"    [DRY RUN] Would migrate project {project['id']} -> {host_path}")
                continue

            # Insert into new projects table
            cursor.execute("""
                INSERT INTO projects_new (host_path, container_path, languages, settings, stats,
                                         status, created_at, updated_at, last_indexed_at)
                VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
            """, (
                project['host_path'],
                project['container_path'],
                project['languages'],
                project['settings'],
                project['stats'],
                project['status'],
                project['created_at'],
                project['updated_at'],
                project['last_indexed_at']
            ))

            # Migrate file_hashes
            cursor.execute("""
                INSERT INTO file_hashes_new (project_path, file_path, content_hash, indexed_at)
                SELECT ?, file_path, content_hash, indexed_at
                FROM file_hashes
                WHERE project_id = ?
            """, (host_path, project['id']))

            # Migrate symbols
            cursor.execute("""
                INSERT INTO symbols_new (id, project_path, name, kind, file_path, line, end_line,
                                        language, signature, parent_name, docstring)
                SELECT id, ?, name, kind, file_path, line, end_line,
                       language, signature, parent_name, docstring
                FROM symbols
                WHERE project_id = ?
            """, (host_path, project['id']))

            # Migrate index_runs
            cursor.execute("""
                INSERT INTO index_runs_new (id, project_path, started_at, completed_at,
                                           files_processed, files_total, chunks_created,
                                           status, error_message)
                SELECT id, ?, started_at, completed_at, files_processed, files_total,
                       chunks_created, status, error_message
                FROM index_runs
                WHERE project_id = ?
            """, (host_path, project['id']))

        if not dry_run:
            # Rename old tables to _old
            cursor.execute("ALTER TABLE projects RENAME TO projects_old")
            cursor.execute("ALTER TABLE file_hashes RENAME TO file_hashes_old")
            cursor.execute("ALTER TABLE symbols RENAME TO symbols_old")
            cursor.execute("ALTER TABLE index_runs RENAME TO index_runs_old")

            # Rename new tables to main names
            cursor.execute("ALTER TABLE projects_new RENAME TO projects")
            cursor.execute("ALTER TABLE file_hashes_new RENAME TO file_hashes")
            cursor.execute("ALTER TABLE symbols_new RENAME TO symbols")
            cursor.execute("ALTER TABLE index_runs_new RENAME TO index_runs")

            conn.commit()
            print("✅ Migration completed successfully!")
            print(f"   Old tables kept as: projects_old, file_hashes_old, symbols_old, index_runs_old")
            print(f"   You can drop them manually if everything works correctly")
        else:
            print("[DRY RUN] Migration would complete successfully")

        return True

    except Exception as e:
        print(f"❌ Migration failed: {e}")
        if not dry_run:
            conn.rollback()
        return False
    finally:
        conn.close()


def _create_new_tables(cursor):
    """Create new schema tables."""
    cursor.executescript("""
        CREATE TABLE IF NOT EXISTS projects (
            host_path TEXT PRIMARY KEY,
            container_path TEXT NOT NULL,
            languages TEXT DEFAULT '[]',
            settings TEXT DEFAULT '{}',
            stats TEXT DEFAULT '{"total_files":0,"indexed_files":0,"total_chunks":0,"total_symbols":0}',
            status TEXT DEFAULT 'created',
            created_at TEXT NOT NULL,
            updated_at TEXT NOT NULL,
            last_indexed_at TEXT
        );

        CREATE TABLE IF NOT EXISTS file_hashes (
            project_path TEXT NOT NULL,
            file_path TEXT NOT NULL,
            content_hash TEXT NOT NULL,
            indexed_at TEXT NOT NULL,
            PRIMARY KEY (project_path, file_path),
            FOREIGN KEY (project_path) REFERENCES projects(host_path) ON DELETE CASCADE
        );

        CREATE TABLE IF NOT EXISTS symbols (
            id TEXT PRIMARY KEY,
            project_path TEXT NOT NULL,
            name TEXT NOT NULL,
            kind TEXT NOT NULL,
            file_path TEXT NOT NULL,
            line INTEGER NOT NULL,
            end_line INTEGER NOT NULL,
            language TEXT NOT NULL,
            signature TEXT,
            parent_name TEXT,
            docstring TEXT,
            FOREIGN KEY (project_path) REFERENCES projects(host_path) ON DELETE CASCADE
        );

        CREATE INDEX IF NOT EXISTS idx_symbols_project_name ON symbols(project_path, name);
        CREATE INDEX IF NOT EXISTS idx_symbols_project_kind ON symbols(project_path, kind);
        CREATE INDEX IF NOT EXISTS idx_symbols_project_file ON symbols(project_path, file_path);

        CREATE TABLE IF NOT EXISTS index_runs (
            id TEXT PRIMARY KEY,
            project_path TEXT NOT NULL,
            started_at TEXT NOT NULL,
            completed_at TEXT,
            files_processed INTEGER DEFAULT 0,
            files_total INTEGER DEFAULT 0,
            chunks_created INTEGER DEFAULT 0,
            status TEXT DEFAULT 'running',
            error_message TEXT,
            FOREIGN KEY (project_path) REFERENCES projects(host_path) ON DELETE CASCADE
        );
    """)


def _create_new_tables_with_suffix(cursor, suffix: str):
    """Create new schema tables with a suffix."""
    cursor.executescript(f"""
        CREATE TABLE projects{suffix} (
            host_path TEXT PRIMARY KEY,
            container_path TEXT NOT NULL,
            languages TEXT DEFAULT '[]',
            settings TEXT DEFAULT '{{}}',
            stats TEXT DEFAULT '{{"total_files":0,"indexed_files":0,"total_chunks":0,"total_symbols":0}}',
            status TEXT DEFAULT 'created',
            created_at TEXT NOT NULL,
            updated_at TEXT NOT NULL,
            last_indexed_at TEXT
        );

        CREATE TABLE file_hashes{suffix} (
            project_path TEXT NOT NULL,
            file_path TEXT NOT NULL,
            content_hash TEXT NOT NULL,
            indexed_at TEXT NOT NULL,
            PRIMARY KEY (project_path, file_path),
            FOREIGN KEY (project_path) REFERENCES projects{suffix}(host_path) ON DELETE CASCADE
        );

        CREATE TABLE symbols{suffix} (
            id TEXT PRIMARY KEY,
            project_path TEXT NOT NULL,
            name TEXT NOT NULL,
            kind TEXT NOT NULL,
            file_path TEXT NOT NULL,
            line INTEGER NOT NULL,
            end_line INTEGER NOT NULL,
            language TEXT NOT NULL,
            signature TEXT,
            parent_name TEXT,
            docstring TEXT,
            FOREIGN KEY (project_path) REFERENCES projects{suffix}(host_path) ON DELETE CASCADE
        );

        CREATE INDEX idx_symbols{suffix}_project_name ON symbols{suffix}(project_path, name);
        CREATE INDEX idx_symbols{suffix}_project_kind ON symbols{suffix}(project_path, kind);
        CREATE INDEX idx_symbols{suffix}_project_file ON symbols{suffix}(project_path, file_path);

        CREATE TABLE index_runs{suffix} (
            id TEXT PRIMARY KEY,
            project_path TEXT NOT NULL,
            started_at TEXT NOT NULL,
            completed_at TEXT,
            files_processed INTEGER DEFAULT 0,
            files_total INTEGER DEFAULT 0,
            chunks_created INTEGER DEFAULT 0,
            status TEXT DEFAULT 'running',
            error_message TEXT,
            FOREIGN KEY (project_path) REFERENCES projects{suffix}(host_path) ON DELETE CASCADE
        );
    """)


def main():
    parser = argparse.ArgumentParser(description="Migrate database from UUID to path-based schema")
    parser.add_argument("--db-path", required=True, help="Path to the SQLite database file")
    parser.add_argument("--dry-run", action="store_true", help="Show what would be done without making changes")

    args = parser.parse_args()

    print("🔄 Starting database migration...")
    print(f"   Database: {args.db_path}")
    print(f"   Dry run: {args.dry_run}")
    print()

    success = asyncio.run(migrate_database(args.db_path, args.dry_run))

    if success:
        print("\n✨ Migration process completed")
        if not args.dry_run:
            print("\n⚠️  IMPORTANT: You should also delete old ChromaDB collections manually if needed")
            print("   Old collection names were: project_<uuid_without_dashes>")
            print("   New collection names are: project_<md5_hash_of_path>")
    else:
        print("\n❌ Migration failed")
        return 1

    return 0


if __name__ == "__main__":
    exit(main())
