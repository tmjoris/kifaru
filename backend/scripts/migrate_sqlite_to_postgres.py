"""Migrate the existing KIFARU SQLite demo database into Neon PostgreSQL."""
import argparse
import os
import sqlite3
import sys
from pathlib import Path

import psycopg
from psycopg import sql

ROOT = Path(__file__).resolve().parents[1]

TABLES = (
    "institutions",
    "reports",
    "validations",
    "alerts",
    "knowledge_base",
    "config",
    "audit_log",
)


def migrate(source: Path, reset: bool) -> None:
    if not source.exists():
        raise FileNotFoundError(f"SQLite source not found: {source}")
    if not os.getenv("DATABASE_URL"):
        raise RuntimeError("DATABASE_URL must contain the Neon PostgreSQL connection string.")

    sqlite_conn = sqlite3.connect(source)
    sqlite_conn.row_factory = sqlite3.Row
    pg_conn = psycopg.connect(os.environ["DATABASE_URL"], autocommit=True)
    if reset:
        pg_conn.execute("""
            DROP TABLE IF EXISTS audit_log, config, knowledge_base, alerts,
              validations, reports, institutions CASCADE
        """)
    for statement in (ROOT / "schema.sql").read_text().split(";"):
        if statement.strip():
            pg_conn.execute(statement)

    pg_conn.execute(sql.SQL("TRUNCATE {} RESTART IDENTITY CASCADE").format(
        sql.SQL(", ").join(map(sql.Identifier, reversed(TABLES)))
    ))

    for table in TABLES:
        rows = sqlite_conn.execute(f"SELECT * FROM {table}").fetchall()
        if not rows:
            print(f"{table}: 0")
            continue
        columns = rows[0].keys()
        query = sql.SQL("INSERT INTO {} ({}) VALUES ({})").format(
            sql.Identifier(table),
            sql.SQL(", ").join(map(sql.Identifier, columns)),
            sql.SQL(", ").join(sql.Placeholder() for _ in columns),
        )
        with pg_conn.cursor() as cursor:
            cursor.executemany(query, [tuple(row[column] for column in columns) for row in rows])
        print(f"{table}: {len(rows)}")

    pg_conn.execute("""
        SELECT setval(
          pg_get_serial_sequence('audit_log', 'id'),
          COALESCE((SELECT MAX(id) FROM audit_log), 1),
          EXISTS (SELECT 1 FROM audit_log)
        )
    """)
    sqlite_conn.close()
    pg_conn.close()


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "--source",
        type=Path,
        default=ROOT / "data" / "kifaru.db",
        help="Path to the existing SQLite database.",
    )
    parser.add_argument(
        "--reset",
        action="store_true",
        help="Drop and recreate PostgreSQL tables before migration.",
    )
    args = parser.parse_args()
    migrate(args.source, args.reset)
