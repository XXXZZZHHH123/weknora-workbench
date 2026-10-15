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

// Released workbench databases used SQLite 32-34 for workspace tables before
// upstream assigned 32-33 to image projection and source positions. Renumbering
// SQL files alone cannot run the upstream migrations those databases skipped.
// Some affected databases already advanced to 35-40 before this repair shipped.
// Keep their version ledger intact and restore the missing schema atomically.
func reconcileSQLiteWorkbenchLineage(ctx context.Context, dsn string, opts MigrationOptions, version uint) error {
	if version < 32 || version > 40 {
		return nil
	}
	path := opts.SQLiteDBPath
	if path == "" {
		parsed, err := url.Parse(dsn)
		if err != nil {
			return err
		}
		path = strings.TrimPrefix(migrate.FilterCustomQuery(parsed).String(), "sqlite3://")
	}
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var tables int
	err = tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table'
  AND name IN ('assistant_projects','assistant_project_sessions','research_workbenches','knowledge_folders','default_knowledge_base_bindings')`).Scan(&tables)
	if err != nil {
		return err
	}
	if tables == 0 {
		return nil
	}
	var positions, projectionObjects int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info('chunks') WHERE name='source_locators'`).Scan(&positions); err != nil {
		return err
	}
	if version == 32 && positions != 0 {
		return fmt.Errorf("unrecognized workbench migration lineage: source positions precede their pending migration")
	}
	if positions != 0 {
		var compatible int
		err = tx.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info('chunks')
   WHERE name='source_locators' AND upper(type)='TEXT' AND "notnull"=0 AND pk=0`).Scan(&compatible)
		if err != nil {
			return err
		}
		if compatible != 1 {
			return fmt.Errorf("unrecognized workbench migration lineage: incompatible chunks.source_locators")
		}
	}
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE name IN
  ('chunk_images','idx_chunk_images_listing','idx_chunk_images_copies','trg_chunk_images_insert',
   'trg_chunk_images_patch','trg_chunk_images_update','trg_chunk_images_delete')`).Scan(&projectionObjects); err != nil {
		return err
	}
	// Healthy upgraded databases must not repeat the full image backfill on
	// every startup. The original 32-34 lineage checks still apply unchanged.
	if version > 34 && positions != 0 && projectionObjects == 7 {
		return nil
	}
	if err = validateSQLiteWorkbenchLineage(ctx, tx, version); err != nil {
		return err
	}
	imageSQL, err := os.ReadFile(filepath.Join("migrations", "sqlite", "000032_chunk_images.up.sql"))
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, string(imageSQL)); err != nil {
		return fmt.Errorf("restore skipped SQLite image migration: %w", err)
	}
	// Version 32 still has upstream 33 pending; let the normal migrator add
	// that column once rather than replaying ALTER TABLE after this repair.
	if version == 32 {
		return tx.Commit()
	}
	if positions == 0 {
		sourceSQL, readErr := os.ReadFile(filepath.Join("migrations", "sqlite", "000033_chunk_source_locators.up.sql"))
		if readErr != nil {
			return readErr
		}
		if _, err = tx.ExecContext(ctx, string(sourceSQL)); err != nil {
			return fmt.Errorf("restore skipped SQLite source migration: %w", err)
		}
	}
	return tx.Commit()
}

// Validate the complete released table definitions, including keys, defaults,
// foreign keys and indexes. A coincidental table name or a partially applied
// schema must never authorize this compatibility path.
func validateSQLiteWorkbenchLineage(ctx context.Context, tx *sql.Tx, version uint) error {
	expected, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		return err
	}
	defer expected.Close()
	expected.SetMaxOpenConns(1)
	files := []string{"000038_workspace_navigation.up.sql"}
	if version >= 33 {
		files = append(files, "000039_knowledge_directories.up.sql")
	}
	if version >= 34 {
		files = append(files, "000040_default_knowledge_base.up.sql")
	}
	for _, name := range files {
		body, readErr := os.ReadFile(filepath.Join("migrations", "sqlite", name))
		if readErr != nil {
			return readErr
		}
		if _, err = expected.ExecContext(ctx, string(body)); err != nil {
			return err
		}
	}
	rows, err := expected.QueryContext(ctx, `SELECT type,name,sql FROM sqlite_master WHERE sql IS NOT NULL`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var kind, name, want, actual string
		if err = rows.Scan(&kind, &name, &want); err != nil {
			return err
		}
		if err = tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type=? AND name=?`, kind, name).Scan(&actual); err != nil {
			return fmt.Errorf("unrecognized workbench migration lineage: missing %s %s: %w", kind, name, err)
		}
		if normalizedSQLiteDDL(actual) != normalizedSQLiteDDL(want) {
			return fmt.Errorf("unrecognized workbench migration lineage: incompatible %s %s", kind, name)
		}
	}
	return rows.Err()
}

func normalizedSQLiteDDL(ddl string) string {
	return strings.ReplaceAll(strings.Join(strings.Fields(strings.ToLower(ddl)), " "), " if not exists", "")
}
