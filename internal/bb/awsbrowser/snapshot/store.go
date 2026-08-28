package snapshot

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jisung9870/binbox-cli/internal/bb/awsbrowser"
	"modernc.org/sqlite"
)

const defaultRetention = 2

type OpenResult struct {
	RecoveredFrom string
}

type Store struct {
	db        *sql.DB
	path      string
	retention int
}

func Open(ctx context.Context, path string, retention int) (*Store, OpenResult, error) {
	if strings.TrimSpace(path) == "" {
		return nil, OpenResult{}, ErrInvalidInput
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, OpenResult{}, err
	}
	if info, statErr := os.Lstat(absPath); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, OpenResult{}, fmt.Errorf("snapshot path is a symlink: %s", absPath)
	} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return nil, OpenResult{}, statErr
	}
	if err := os.MkdirAll(filepath.Dir(absPath), 0o700); err != nil {
		return nil, OpenResult{}, err
	}
	if retention <= 0 {
		retention = defaultRetention
	}

	db, integrityErr := openAndCheck(ctx, absPath)
	result := OpenResult{}
	if integrityErr != nil {
		if db != nil {
			_ = db.Close()
		}
		if !errors.Is(integrityErr, ErrCorruptStore) {
			return nil, result, integrityErr
		}
		if _, statErr := os.Stat(absPath); statErr != nil {
			return nil, result, integrityErr
		}
		recovered, recoveryErr := quarantineCorruptFiles(absPath, time.Now().UTC())
		if recoveryErr != nil {
			return nil, result, errors.Join(integrityErr, recoveryErr)
		}
		result.RecoveredFrom = recovered
		db, integrityErr = openAndCheck(ctx, absPath)
		if integrityErr != nil {
			if db != nil {
				_ = db.Close()
			}
			return nil, result, integrityErr
		}
	}

	store := &Store{db: db, path: absPath, retention: retention}
	if err := store.initSchema(ctx); err != nil {
		_ = db.Close()
		return nil, result, err
	}
	if err := os.Chmod(absPath, 0o600); err != nil {
		_ = db.Close()
		return nil, result, err
	}
	return store, result, nil
}

func openAndCheck(ctx context.Context, path string) (*sql.DB, error) {
	values := url.Values{}
	values.Set("_busy_timeout", "5000")
	values.Set("_defensive", "1")
	values.Set("_dqs", "0")
	values.Set("_foreign_keys", "on")
	values.Set("_journal_mode", "WAL")
	values.Set("_synchronous", "FULL")
	values.Set("_txlock", "immediate")
	dsn := (&url.URL{Scheme: "file", Path: path, RawQuery: values.Encode()}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		return db, classifySQLiteError(err)
	}
	var integrity string
	if err := db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity); err != nil {
		return db, classifySQLiteError(err)
	}
	if integrity != "ok" {
		return db, fmt.Errorf("%w: integrity check failed: %s", ErrCorruptStore, integrity)
	}
	return db, nil
}

func classifySQLiteError(err error) error {
	var sqliteError *sqlite.Error
	if errors.As(err, &sqliteError) {
		switch sqliteError.Code() & 0xff {
		case 11, 26: // SQLITE_CORRUPT, SQLITE_NOTADB
			return fmt.Errorf("%w: %v", ErrCorruptStore, err)
		}
	}
	return err
}

func quarantineCorruptFiles(path string, now time.Time) (string, error) {
	suffix := ".corrupt-" + now.Format("20060102T150405.000000000Z")
	recovered := path + suffix
	if err := os.Rename(path, recovered); err != nil {
		return "", err
	}
	for _, sidecar := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(path + sidecar); err == nil {
			if err := os.Rename(path+sidecar, recovered+sidecar); err != nil {
				return "", err
			}
		}
	}
	return recovered, nil
}

func (store *Store) initSchema(ctx context.Context) error {
	var currentVersion int
	if err := store.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&currentVersion); err != nil {
		return err
	}
	if currentVersion != 0 && currentVersion != SchemaVersion {
		return fmt.Errorf("%w: got %d, want %d", ErrSchemaVersion, currentVersion, SchemaVersion)
	}
	const schema = `
PRAGMA user_version = 1;
CREATE TABLE IF NOT EXISTS metadata (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
) STRICT;
CREATE TABLE IF NOT EXISTS snapshot_run (
  id TEXT PRIMARY KEY,
  started_at TEXT NOT NULL,
  completed_at TEXT NOT NULL,
  schema_version INTEGER NOT NULL,
  status TEXT NOT NULL CHECK (status = 'complete')
) STRICT;
CREATE TABLE IF NOT EXISTS coverage (
  run_id TEXT NOT NULL REFERENCES snapshot_run(id) ON DELETE CASCADE,
  profile TEXT NOT NULL,
  account_id TEXT NOT NULL,
  region TEXT NOT NULL,
  service TEXT NOT NULL,
  status TEXT NOT NULL CHECK (status IN ('succeeded','failed','not-observed')),
  error_kind TEXT NOT NULL,
  PRIMARY KEY (run_id, profile, account_id, region, service)
) STRICT;
CREATE TABLE IF NOT EXISTS resource (
	  id INTEGER PRIMARY KEY,
	  resource_key TEXT NOT NULL UNIQUE,
  partition TEXT NOT NULL,
  account_id TEXT NOT NULL,
  region TEXT NOT NULL,
  resource_type TEXT NOT NULL,
  resource_id TEXT NOT NULL,
  name TEXT NOT NULL
) STRICT;
CREATE TABLE IF NOT EXISTS observation (
  run_id TEXT NOT NULL REFERENCES snapshot_run(id) ON DELETE CASCADE,
	  resource_id INTEGER NOT NULL REFERENCES resource(id),
  profile TEXT NOT NULL,
  account_id TEXT NOT NULL,
  region TEXT NOT NULL,
  observed_at TEXT NOT NULL,
	  PRIMARY KEY (run_id, resource_id, profile, account_id, region)
) STRICT;
CREATE TABLE IF NOT EXISTS relation (
  id INTEGER PRIMARY KEY,
  run_id TEXT NOT NULL REFERENCES snapshot_run(id) ON DELETE CASCADE,
	  source_id INTEGER NOT NULL REFERENCES resource(id),
	  target_id INTEGER NOT NULL REFERENCES resource(id),
  relation_type TEXT NOT NULL,
  direction TEXT NOT NULL CHECK (direction = 'outgoing'),
  confidence TEXT NOT NULL,
  condition TEXT NOT NULL,
  reason TEXT NOT NULL,
  operation TEXT NOT NULL,
  scope TEXT NOT NULL,
  observed_at TEXT NOT NULL,
	  UNIQUE (run_id, source_id, target_id, relation_type, condition, operation)
) STRICT;
	CREATE INDEX IF NOT EXISTS relation_source_idx ON relation(run_id, source_id);
	CREATE INDEX IF NOT EXISTS relation_target_idx ON relation(run_id, target_id);
	CREATE INDEX IF NOT EXISTS observation_resource_idx ON observation(run_id, resource_id);
CREATE INDEX IF NOT EXISTS coverage_scope_idx ON coverage(run_id, account_id, region, service);
`
	_, err := store.db.ExecContext(ctx, schema)
	return err
}

func (store *Store) Close() error {
	if store == nil || store.db == nil {
		return nil
	}
	return store.db.Close()
}

func (store *Store) CommitRun(ctx context.Context, input RunInput) (Run, error) {
	if store == nil || store.db == nil {
		return Run{}, ErrInvalidInput
	}
	if err := input.validate(); err != nil {
		return Run{}, err
	}
	runID, err := newRunID()
	if err != nil {
		return Run{}, err
	}
	run := Run{ID: runID, StartedAt: input.StartedAt.UTC(), CompletedAt: input.CompletedAt.UTC(), SchemaVersion: SchemaVersion}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return Run{}, err
	}
	defer tx.Rollback()

	if _, err = tx.ExecContext(ctx, `INSERT INTO snapshot_run(id,started_at,completed_at,schema_version,status) VALUES(?,?,?,?,?)`,
		run.ID, formatTime(run.StartedAt), formatTime(run.CompletedAt), run.SchemaVersion, "complete"); err != nil {
		return Run{}, err
	}
	resourceStatement, err := tx.PrepareContext(ctx, `INSERT INTO resource(resource_key,partition,account_id,region,resource_type,resource_id,name)
VALUES(?,?,?,?,?,?,?) ON CONFLICT(resource_key) DO UPDATE SET name=CASE WHEN excluded.name<>'' THEN excluded.name ELSE resource.name END RETURNING id`)
	if err != nil {
		return Run{}, err
	}
	defer resourceStatement.Close()
	resourceIDs := make(map[string]int64, len(input.Resources))
	ensureResource := func(resource Resource) (int64, error) {
		key, _ := resource.Ref.Key()
		if id, ok := resourceIDs[key]; ok {
			if resource.Name != "" {
				if err := resourceStatement.QueryRowContext(ctx, key, resource.Ref.Partition, resource.Ref.AccountID, resource.Ref.Region, resource.Ref.Type, resource.Ref.ID, resource.Name).Scan(&id); err != nil {
					return 0, err
				}
				resourceIDs[key] = id
			}
			return id, nil
		}
		var id int64
		if err := resourceStatement.QueryRowContext(ctx, key, resource.Ref.Partition, resource.Ref.AccountID, resource.Ref.Region, resource.Ref.Type, resource.Ref.ID, resource.Name).Scan(&id); err != nil {
			return 0, err
		}
		resourceIDs[key] = id
		return id, nil
	}
	for _, resource := range input.Resources {
		if _, err = ensureResource(resource); err != nil {
			return Run{}, err
		}
	}
	observationStatement, err := tx.PrepareContext(ctx, `INSERT INTO observation(run_id,resource_id,profile,account_id,region,observed_at)
VALUES(?,?,?,?,?,?) ON CONFLICT DO NOTHING`)
	if err != nil {
		return Run{}, err
	}
	defer observationStatement.Close()
	for _, observation := range input.Observations {
		resourceID, ensureErr := ensureResource(Resource{Ref: observation.Resource})
		if ensureErr != nil {
			return Run{}, ensureErr
		}
		if _, err = observationStatement.ExecContext(ctx, run.ID, resourceID, observation.Profile, observation.AccountID, observation.Region, formatTime(observation.ObservedAt)); err != nil {
			return Run{}, err
		}
	}
	relationStatement, err := tx.PrepareContext(ctx, `INSERT INTO relation(run_id,source_id,target_id,relation_type,direction,confidence,condition,reason,operation,scope,observed_at)
VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT DO NOTHING`)
	if err != nil {
		return Run{}, err
	}
	defer relationStatement.Close()
	for _, relation := range input.Relations {
		sourceID, ensureErr := ensureResource(Resource{Ref: relation.Source})
		if ensureErr != nil {
			return Run{}, ensureErr
		}
		targetID, ensureErr := ensureResource(Resource{Ref: relation.Target})
		if ensureErr != nil {
			return Run{}, ensureErr
		}
		if _, err = relationStatement.ExecContext(ctx, run.ID, sourceID, targetID, relation.Type, relation.Direction, relation.Confidence,
			relation.Condition, relation.Reason, relation.Operation, relation.Scope, formatTime(relation.ObservedAt)); err != nil {
			return Run{}, err
		}
	}
	coverageStatement, err := tx.PrepareContext(ctx, `INSERT INTO coverage(run_id,profile,account_id,region,service,status,error_kind) VALUES(?,?,?,?,?,?,?)`)
	if err != nil {
		return Run{}, err
	}
	defer coverageStatement.Close()
	for _, coverage := range input.Coverage {
		if _, err = coverageStatement.ExecContext(ctx,
			run.ID, coverage.Profile, coverage.AccountID, coverage.Region, coverage.Service, coverage.Status, coverage.ErrorKind); err != nil {
			return Run{}, err
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO metadata(key,value) VALUES('active_run',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, run.ID); err != nil {
		return Run{}, err
	}
	if err = store.retainRuns(ctx, tx); err != nil {
		return Run{}, err
	}
	if err = tx.Commit(); err != nil {
		return Run{}, err
	}
	return run, nil
}

func (store *Store) retainRuns(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM snapshot_run WHERE id IN (
	SELECT id FROM snapshot_run
	WHERE id <> (SELECT value FROM metadata WHERE key='active_run')
	ORDER BY completed_at DESC, id DESC LIMIT -1 OFFSET ?
)`, store.retention-1); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM resource
	WHERE id NOT IN (SELECT resource_id FROM observation)
	  AND id NOT IN (SELECT source_id FROM relation)
	  AND id NOT IN (SELECT target_id FROM relation)`)
	return err
}

func (store *Store) ActiveRun(ctx context.Context) (Run, error) {
	const query = `SELECT r.id,r.started_at,r.completed_at,r.schema_version
FROM snapshot_run r JOIN metadata m ON m.key='active_run' AND m.value=r.id`
	var run Run
	var started, completed string
	if err := store.db.QueryRowContext(ctx, query).Scan(&run.ID, &started, &completed, &run.SchemaVersion); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Run{}, ErrNoActiveRun
		}
		return Run{}, err
	}
	var err error
	if run.StartedAt, err = parseTime(started); err != nil {
		return Run{}, err
	}
	if run.CompletedAt, err = parseTime(completed); err != nil {
		return Run{}, err
	}
	return run, nil
}

func (store *Store) Coverage(ctx context.Context) ([]Coverage, error) {
	run, err := store.ActiveRun(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := store.db.QueryContext(ctx, `SELECT profile,account_id,region,service,status,error_kind FROM coverage WHERE run_id=? ORDER BY profile,region,service`, run.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Coverage
	for rows.Next() {
		var item Coverage
		if err := rows.Scan(&item.Profile, &item.AccountID, &item.Region, &item.Service, &item.Status, &item.ErrorKind); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (store *Store) Outgoing(ctx context.Context, source ResourceRef, limit int) ([]Edge, error) {
	key, err := source.Key()
	if err != nil {
		return nil, err
	}
	return store.queryEdges(ctx, "r.source_id", key, limit)
}

func (store *Store) Reverse(ctx context.Context, target ResourceRef, limit int) ([]Edge, error) {
	key, err := target.Key()
	if err != nil {
		return nil, err
	}
	edges, err := store.queryEdges(ctx, "r.target_id", key, limit)
	if err != nil {
		return nil, err
	}
	for index := range edges {
		edges[index].Relation.Direction = awsbrowser.RelationIncoming
	}
	return edges, nil
}

func (store *Store) queryEdges(ctx context.Context, column, key string, limit int) ([]Edge, error) {
	if limit <= 0 || limit > 10000 {
		limit = 1000
	}
	run, err := store.ActiveRun(ctx)
	if err != nil {
		return nil, err
	}
	resourceID, err := store.resourceID(ctx, key)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	query := `SELECT r.run_id,s.resource_key,t.resource_key,r.relation_type,r.direction,r.confidence,r.condition,r.reason,r.operation,r.scope,r.observed_at,
	s.name,t.name
FROM relation r
	JOIN resource s ON s.id=r.source_id
	JOIN resource t ON t.id=r.target_id
WHERE r.run_id=? AND ` + column + `=? ORDER BY r.relation_type,r.source_id,r.target_id LIMIT ?`
	rows, err := store.db.QueryContext(ctx, query, run.ID, resourceID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Edge
	for rows.Next() {
		var edge Edge
		var observed string
		if err := rows.Scan(&edge.RunID, &edge.SourceKey, &edge.TargetKey, &edge.Relation.Type, &edge.Relation.Direction,
			&edge.Relation.Confidence, &edge.Relation.Condition, &edge.Relation.Reason, &edge.Relation.Operation,
			&edge.Relation.Scope, &observed, &edge.SourceName, &edge.TargetName); err != nil {
			return nil, err
		}
		edge.Relation.ObservedAt, err = parseTime(observed)
		if err != nil {
			return nil, err
		}
		result = append(result, edge)
	}
	return result, rows.Err()
}

// FindPath is a bounded graph-storage PoC query, not a packet reachability
// claim. It traverses exact stored outgoing edges breadth-first in the active
// run and caps visited nodes so a dense or hostile graph cannot grow without
// bound.
func (store *Store) FindPath(ctx context.Context, source, target ResourceRef, maxDepth int) ([]string, error) {
	if maxDepth <= 0 || maxDepth > 32 {
		return nil, ErrInvalidInput
	}
	sourceKey, err := source.Key()
	if err != nil {
		return nil, err
	}
	targetKey, err := target.Key()
	if err != nil {
		return nil, err
	}
	run, err := store.ActiveRun(ctx)
	if err != nil {
		return nil, err
	}
	if sourceKey == targetKey {
		return []string{sourceKey}, nil
	}
	const maxVisited = 100_000
	sourceID, err := store.resourceID(ctx, sourceKey)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	targetID, err := store.resourceID(ctx, targetKey)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	parents := map[int64]int64{sourceID: 0}
	frontier := []int64{sourceID}
	for depth := 0; depth < maxDepth && len(frontier) > 0; depth++ {
		query := `SELECT source_id,target_id FROM relation INDEXED BY relation_source_idx
	WHERE run_id=? AND source_id IN (` + placeholders(len(frontier)) + `)
  AND confidence IN ('id-exact','api-exact')
ORDER BY source_id,target_id`
		arguments := make([]any, 0, len(frontier)+1)
		arguments = append(arguments, run.ID)
		for _, id := range frontier {
			arguments = append(arguments, id)
		}
		rows, queryErr := store.db.QueryContext(ctx, query, arguments...)
		if queryErr != nil {
			return nil, queryErr
		}
		next := make([]int64, 0, len(frontier))
		found := false
		for rows.Next() {
			var from, to int64
			if err := rows.Scan(&from, &to); err != nil {
				_ = rows.Close()
				return nil, err
			}
			if _, seen := parents[to]; seen {
				continue
			}
			parents[to] = from
			if len(parents) > maxVisited {
				_ = rows.Close()
				return nil, ErrTraversalLimit
			}
			next = append(next, to)
			if to == targetID {
				found = true
				break
			}
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		if found {
			ids := rebuildPath(parents, targetID)
			path := make([]string, len(ids))
			for index, id := range ids {
				if err := store.db.QueryRowContext(ctx, `SELECT resource_key FROM resource WHERE id=?`, id).Scan(&path[index]); err != nil {
					return nil, err
				}
			}
			return path, nil
		}
		frontier = next
	}
	return nil, nil
}

func placeholders(count int) string {
	return strings.TrimSuffix(strings.Repeat("?,", count), ",")
}

func rebuildPath(parents map[int64]int64, target int64) []int64 {
	path := []int64{target}
	for parents[path[len(path)-1]] != 0 {
		path = append(path, parents[path[len(path)-1]])
	}
	for left, right := 0, len(path)-1; left < right; left, right = left+1, right-1 {
		path[left], path[right] = path[right], path[left]
	}
	return path
}

func (store *Store) resourceID(ctx context.Context, key string) (int64, error) {
	var id int64
	err := store.db.QueryRowContext(ctx, `SELECT id FROM resource WHERE resource_key=?`, key).Scan(&id)
	return id, err
}

func (store *Store) IntegrityCheck(ctx context.Context) error {
	var result string
	if err := store.db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&result); err != nil {
		return err
	}
	if result != "ok" {
		return fmt.Errorf("sqlite integrity check failed: %s", result)
	}
	rows, err := store.db.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		return errors.New("sqlite foreign key check failed")
	}
	return rows.Err()
}

func newRunID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

func formatTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func parseTime(value string) (time.Time, error) {
	return time.Parse(time.RFC3339Nano, value)
}
