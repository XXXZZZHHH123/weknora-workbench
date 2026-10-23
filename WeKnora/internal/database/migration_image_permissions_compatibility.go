package database

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/golang-migrate/migrate/v4"
)

// Upstream used SQLite 42 / PostgreSQL 124 for image generation while released
// workbench databases used those versions for resource permissions. Keep the
// permission lineage stable and move the idempotent image migration to 47/129.
// Databases from upstream have already skipped the permission migration: repair
// only its missing columns in a transaction before later ACL migrations run.
// Never force a version or replace rows, policies, or existing column values.
func reconcileImageGenerationPermissionLineage(ctx context.Context, dsn string, opts MigrationOptions, version uint) error {
	sqlite := strings.HasPrefix(dsn, "sqlite3://")
	if (sqlite && version < 42) || (!sqlite && version < 124) {
		return nil
	}
	driver := "postgres"
	dbDSN := dsn
	if sqlite {
		driver = "sqlite3"
		dbDSN = opts.SQLiteDBPath
		if dbDSN == "" {
			parsed, err := url.Parse(dsn)
			if err != nil {
				return err
			}
			dbDSN = strings.TrimPrefix(migrate.FilterCustomQuery(parsed).String(), "sqlite3://")
		}
	}
	db, err := sql.Open(driver, dbDSN)
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var imageTableExists bool
	imageTableQuery := `SELECT to_regclass('image_generation_executions') IS NOT NULL`
	if sqlite {
		imageTableQuery = `SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='image_generation_executions')`
	}
	if err = tx.QueryRowContext(ctx, imageTableQuery).Scan(&imageTableExists); err != nil {
		return err
	}
	if !imageTableExists {
		return nil
	}

	columns := []struct{ table, column, sqliteDDL string }{
		{"knowledge_bases", "access_policy", `ALTER TABLE knowledge_bases ADD COLUMN access_policy TEXT`},
		{"research_workbenches", "access_policy", `ALTER TABLE research_workbenches ADD COLUMN access_policy TEXT`},
		{"research_workbenches", "source_sharing_approved", `ALTER TABLE research_workbenches ADD COLUMN source_sharing_approved BOOLEAN NOT NULL DEFAULT FALSE`},
		{"research_workbenches", "source_sharing_approved_by", `ALTER TABLE research_workbenches ADD COLUMN source_sharing_approved_by TEXT NOT NULL DEFAULT ''`},
		{"research_workbenches", "source_sharing_requested", `ALTER TABLE research_workbenches ADD COLUMN source_sharing_requested BOOLEAN NOT NULL DEFAULT FALSE`},
	}
	missing := false
	for _, column := range columns {
		var exists bool
		if sqlite {
			err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pragma_table_info(?) WHERE name=?)`, column.table, column.column).Scan(&exists)
		} else {
			err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name=$1 AND column_name=$2)`, column.table, column.column).Scan(&exists)
		}
		if err != nil {
			return err
		}
		if exists {
			continue
		}
		missing = true
		if sqlite {
			if _, err = tx.ExecContext(ctx, column.sqliteDDL); err != nil {
				return fmt.Errorf("restore skipped resource permission column %s.%s: %w", column.table, column.column, err)
			}
		}
	}
	if !sqlite && missing {
		body, readErr := os.ReadFile(filepath.Join("migrations", "versioned", "000124_resource_permissions.up.sql"))
		if readErr != nil {
			return readErr
		}
		if _, err = tx.ExecContext(ctx, string(body)); err != nil {
			return fmt.Errorf("restore skipped resource permission migration: %w", err)
		}
	}
	return tx.Commit()
}
