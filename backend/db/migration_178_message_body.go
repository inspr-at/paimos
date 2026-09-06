// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"

	"github.com/inspr-at/paimos/backend/safetext"
	"modernc.org/sqlite"
)

func paimosMessageBodyContainsSecretLikeSQL(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
	if len(args) != 1 || args[0] == nil {
		return int64(1), nil
	}
	var value string
	switch v := args[0].(type) {
	case string:
		value = v
	case []byte:
		value = string(v)
	default:
		return int64(1), nil
	}
	if safetext.MessageBodyContainsSecretLike(value) {
		return int64(1), nil
	}
	return int64(0), nil
}

func applyMessageBodyMigration178(ctx context.Context, conn *sql.Conn) (err error) {
	// Create-copy-drop-rename keeps every child FK aimed at agent_messages.
	// Legacy rename mode also leaves the other tables' triggers/views intact
	// while the original name is briefly absent inside this transaction.
	var foreignKeys, legacyAlter int
	if err = conn.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		return err
	}
	if err = conn.QueryRowContext(ctx, `PRAGMA legacy_alter_table`).Scan(&legacyAlter); err != nil {
		return err
	}
	defer func() {
		_, fkErr := conn.ExecContext(context.Background(), fmt.Sprintf(`PRAGMA foreign_keys=%d`, foreignKeys))
		_, legacyErr := conn.ExecContext(context.Background(), fmt.Sprintf(`PRAGMA legacy_alter_table=%d`, legacyAlter))
		err = errors.Join(err, fkErr, legacyErr)
	}()
	if _, err = conn.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		return err
	}
	if _, err = conn.ExecContext(ctx, `PRAGMA legacy_alter_table=ON`); err != nil {
		return err
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var tableSQL string
	if err = tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name='agent_messages'`).Scan(&tableSQL); err != nil {
		return err
	}
	const oldGuard = `CHECK(NOT paimos_contains_secret_like(body))`
	const newGuard = `CHECK(NOT paimos_message_body_contains_secret_like(CAST(body AS BLOB)))`
	const prefix = `CREATE TABLE agent_messages (`
	if !strings.HasPrefix(tableSQL, prefix) || strings.Count(tableSQL, oldGuard) != 1 {
		return fmt.Errorf("M178 requires the existing message-body CHECK")
	}
	var unsafeRows int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_messages
		WHERE paimos_message_body_contains_secret_like(CAST(body AS BLOB))<>0`).Scan(&unsafeRows); err != nil {
		return err
	}
	if unsafeRows != 0 {
		return fmt.Errorf("M178 blocked by %d legacy message bodies failing the message security guard", unsafeRows)
	}
	// Keep all columns, constraints, indexes and table-owned triggers from the
	// actual prior schema, including later reply and human-session additions.
	objects, err := tx.QueryContext(ctx, `SELECT sql FROM sqlite_master
		WHERE tbl_name='agent_messages' AND type IN ('index','trigger') AND sql IS NOT NULL ORDER BY type,name`)
	if err != nil {
		return err
	}
	var restore []string
	for objects.Next() {
		var statement string
		if err = objects.Scan(&statement); err != nil {
			objects.Close()
			return err
		}
		restore = append(restore, statement)
	}
	err = objects.Err()
	objects.Close()
	if err != nil {
		return err
	}
	var sequence int64
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT seq FROM sqlite_sequence WHERE name='agent_messages'),0)`).Scan(&sequence); err != nil {
		return err
	}
	createSQL := strings.Replace(tableSQL, prefix, `CREATE TABLE agent_messages_m178 (`, 1)
	createSQL = strings.Replace(createSQL, oldGuard, newGuard, 1)
	steps := append([]string{
		createSQL,
		`INSERT INTO agent_messages_m178 SELECT * FROM agent_messages`,
		`DROP TABLE agent_messages`,
		`ALTER TABLE agent_messages_m178 RENAME TO agent_messages`,
	}, restore...)
	for _, step := range steps {
		if _, err = tx.ExecContext(ctx, step); err != nil {
			return migrationStepError(178, step, err)
		}
	}
	// Copying surviving rows alone can regress a deleted cursor's high-water.
	if _, err = tx.ExecContext(ctx, `DELETE FROM sqlite_sequence WHERE name='agent_messages'`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO sqlite_sequence(name,seq) VALUES('agent_messages',?)`, sequence); err != nil {
		return err
	}
	var violations int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil {
		return err
	}
	if violations != 0 {
		return fmt.Errorf("M178 foreign-key validation failed")
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO schema_versions(version) VALUES(178)`); err != nil {
		return err
	}
	return tx.Commit()
}
