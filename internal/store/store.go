// Package store is the SQLite-backed metadata library.
//
// The library keeps one row per image (identity and the few facts the
// resolution rules need), one row per scanned source directory, and one row
// per group with the resolved plan. It is intentionally lean: no pixels, no
// full EXIF blobs.
package store

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// Image is one scanned picture.
type Image struct {
	ID          string
	SourceID    int64
	RelPath     string
	Name        string
	SHA256      string
	Phash       string // candidate DCT hex (primary phash); "" when unhashable
	CreatedAt   int64  // unix seconds, file birth time (mtime fallback)
	MTime       int64  // unix nanoseconds, file modification time (fingerprint cache key)
	ExifCapture string // raw EXIF capture wall clock, e.g. "2020:05:01 12:30:00"
	ExifOffset  string // EXIF timezone offset, e.g. "+05:30" or "Z"; "" when unknown
	ExifCamera  string // camera block (EXIF Make + Model), "" when unknown
	Width       int    // displayed width (after EXIF orientation)
	Height      int
	Kind        string // jpeg|png|gif|webp|tiff|heic
	Size        int64
	HasGPS      bool
	GPSLat      float64
	GPSLon      float64
	GroupID     int64 // 0 until the group phase runs
}

// Group is the resolved plan for one group of pictures.
type Group struct {
	ID              int64
	Status          string // resolved|conflict|singleton
	Reason          string
	ResolvedBy      string // "rules" or the resolver that settled the conflict
	PixelImageID    string
	PixelPhash      string // perceptual hash of the selected pixel file
	CaptureImageID  string
	CaptureTime     string
	CaptureInstant  int64
	CaptureFallback bool
	HasGPS          bool
	GPSLat          float64
	GPSLon          float64
	StoredName      string
}

// Store is an open library.
type Store struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS sources (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  path TEXT NOT NULL UNIQUE,
  added_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS images (
  id TEXT PRIMARY KEY,
  source_id INTEGER NOT NULL REFERENCES sources(id),
  rel_path TEXT NOT NULL,
  name TEXT NOT NULL,
  sha256 TEXT NOT NULL DEFAULT '',
  phash TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL DEFAULT 0,
  exif_capture TEXT NOT NULL DEFAULT '',
  exif_offset TEXT NOT NULL DEFAULT '',
  exif_camera TEXT NOT NULL DEFAULT '',
  width INTEGER NOT NULL DEFAULT 0,
  height INTEGER NOT NULL DEFAULT 0,
  kind TEXT NOT NULL DEFAULT '',
  size INTEGER NOT NULL DEFAULT 0,
  mtime INTEGER NOT NULL DEFAULT 0,
  gps_lat REAL NOT NULL DEFAULT 0,
  gps_lon REAL NOT NULL DEFAULT 0,
  has_gps INTEGER NOT NULL DEFAULT 0,
  group_id INTEGER NOT NULL DEFAULT 0,
  UNIQUE (source_id, rel_path)
);
CREATE INDEX IF NOT EXISTS idx_images_sha256 ON images(sha256);
CREATE INDEX IF NOT EXISTS idx_images_phash ON images(phash);
CREATE INDEX IF NOT EXISTS idx_images_group ON images(group_id);

CREATE TABLE IF NOT EXISTS groups (
  id INTEGER PRIMARY KEY,
  status TEXT NOT NULL,
  reason TEXT NOT NULL DEFAULT '',
  resolved_by TEXT NOT NULL DEFAULT 'rules',
  pixel_image_id TEXT NOT NULL DEFAULT '',
  pixel_phash TEXT NOT NULL DEFAULT '',
  capture_image_id TEXT NOT NULL DEFAULT '',
  capture_time TEXT NOT NULL DEFAULT '',
  capture_instant INTEGER NOT NULL DEFAULT 0,
  capture_fallback INTEGER NOT NULL DEFAULT 0,
  gps_lat REAL NOT NULL DEFAULT 0,
  gps_lon REAL NOT NULL DEFAULT 0,
  has_gps INTEGER NOT NULL DEFAULT 0,
  stored_name TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS commits (
  id INTEGER PRIMARY KEY,
  at INTEGER NOT NULL,
  summary TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS commit_groups (
  commit_id INTEGER NOT NULL REFERENCES commits(id),
  group_id INTEGER NOT NULL,
  status TEXT NOT NULL,
  reason TEXT NOT NULL DEFAULT '',
  resolved_by TEXT NOT NULL DEFAULT 'rules',
  pixel_image_id TEXT NOT NULL DEFAULT '',
  pixel_phash TEXT NOT NULL DEFAULT '',
  capture_image_id TEXT NOT NULL DEFAULT '',
  capture_time TEXT NOT NULL DEFAULT '',
  capture_instant INTEGER NOT NULL DEFAULT 0,
  capture_fallback INTEGER NOT NULL DEFAULT 0,
  gps_lat REAL NOT NULL DEFAULT 0,
  gps_lon REAL NOT NULL DEFAULT 0,
  has_gps INTEGER NOT NULL DEFAULT 0,
  stored_name TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (commit_id, group_id)
);

CREATE TABLE IF NOT EXISTS commit_actions (
  commit_id INTEGER NOT NULL REFERENCES commits(id),
  group_id INTEGER NOT NULL,
  action TEXT NOT NULL,
  prev_stored_name TEXT NOT NULL DEFAULT '',
  new_stored_name TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_commit_actions_commit ON commit_actions(commit_id);

CREATE TABLE IF NOT EXISTS content_phash (
  sha256 TEXT NOT NULL,
  alg TEXT NOT NULL,
  size INTEGER NOT NULL,
  hex TEXT NOT NULL,
  PRIMARY KEY (sha256, alg, size)
);
CREATE INDEX IF NOT EXISTS idx_content_phash_sha ON content_phash(sha256);

CREATE TABLE IF NOT EXISTS group_outputs (
  target TEXT NOT NULL,
  group_id INTEGER NOT NULL,
  pixel_image_id TEXT NOT NULL DEFAULT '',
  name TEXT NOT NULL,
  sha256 TEXT NOT NULL DEFAULT '',
  facts TEXT NOT NULL DEFAULT '',
  conflict TEXT NOT NULL DEFAULT '',
  prefixed INTEGER NOT NULL DEFAULT 0,
  written_at INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (target, group_id)
);
`

// Open opens (creating if needed) the SQLite library at path.
// Use ":memory:" for a throwaway library.
func Open(path string) (*Store, error) {
	dsn := path
	if path != ":memory:" {
		dsn = "file:" + path
	}
	db, err := sql.Open("sqlite", dsn+"?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, fmt.Errorf("open library %q: %w", path, err)
	}
	if path == ":memory:" {
		// Each pooled connection would get its own private memory database.
		db.SetMaxOpenConns(1)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("init schema: %w", err)
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate schema: %w", err)
	}
	return &Store{db: db}, nil
}

// migrate adds columns that libraries created before them lack. Fresh
// databases already have every column and the check is a no-op.
func migrate(db *sql.DB) error {
	for _, add := range []struct {
		table, col, def string
	}{
		{"images", "gps_lat", "REAL NOT NULL DEFAULT 0"},
		{"images", "gps_lon", "REAL NOT NULL DEFAULT 0"},
		{"images", "has_gps", "INTEGER NOT NULL DEFAULT 0"},
		{"groups", "pixel_phash", "TEXT NOT NULL DEFAULT ''"},
		{"groups", "resolved_by", "TEXT NOT NULL DEFAULT 'rules'"},
		{"images", "mtime", "INTEGER NOT NULL DEFAULT 0"},
		{"images", "exif_camera", "TEXT NOT NULL DEFAULT ''"},
		{"group_outputs", "conflict", "TEXT NOT NULL DEFAULT ''"},
	} {
		have, err := columnExists(db, add.table, add.col)
		if err != nil {
			return err
		}
		if !have {
			if _, err := db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", add.table, add.col, add.def)); err != nil {
				return err
			}
		}
	}
	return nil
}

func columnExists(db *sql.DB, table, col string) (bool, error) {
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cid, notnull, pk int
			name, ctype      string
			dflt             any
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == col {
			return true, nil
		}
	}
	return false, rows.Err()
}

// Close closes the library.
func (s *Store) Close() error { return s.db.Close() }

// Begin starts a transaction on the underlying database.
func (s *Store) Begin() (*sql.Tx, error) { return s.db.Begin() }

// NewID builds an image id from a timestamp (unix millis) and a sequence
// number, per the library design.
func NewID(start time.Time, seq int) string {
	return fmt.Sprintf("%d-%06d", start.UnixMilli(), seq)
}

// FindSource reports whether dir was recorded before and returns its id.
func (s *Store) FindSource(dir string) (int64, bool, error) {
	var id int64
	err := s.db.QueryRow(`SELECT id FROM sources WHERE path = ?`, dir).Scan(&id)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return id, true, nil
}

// AddSource records a source directory and returns its id.
func (s *Store) AddSource(dir string) (int64, error) {
	res, err := s.db.Exec(`INSERT OR IGNORE INTO sources (path, added_at) VALUES (?, ?)`, dir, time.Now().Unix())
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if id == 0 { // already present
		id, _, err = s.FindSource(dir)
	}
	return id, err
}

// Sources returns the recorded source directories by id.
func (s *Store) Sources() (map[int64]string, error) {
	rows, err := s.db.Query(`SELECT id, path FROM sources`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var id int64
		var path string
		if err := rows.Scan(&id, &path); err != nil {
			return nil, err
		}
		out[id] = path
	}
	return out, rows.Err()
}

// UpsertImage inserts img (img.ID must be set) or, when an image with the
// same (source, rel_path) already exists, updates its mutable fields and
// keeps the original id and group assignment.
func (s *Store) UpsertImage(img Image) (id string, created bool, err error) {
	err = s.db.QueryRow(`SELECT id FROM images WHERE source_id = ? AND rel_path = ?`, img.SourceID, img.RelPath).Scan(&id)
	if err == nil {
		_, err = s.db.Exec(`
			UPDATE images SET
			  name = ?, sha256 = ?, phash = ?, created_at = ?,
			  exif_capture = ?, exif_offset = ?, exif_camera = ?,
			  width = ?, height = ?, kind = ?, size = ?, mtime = ?,
			  gps_lat = ?, gps_lon = ?, has_gps = ?
			WHERE id = ?`,
			img.Name, img.SHA256, img.Phash, img.CreatedAt,
			img.ExifCapture, img.ExifOffset, img.ExifCamera,
			img.Width, img.Height, img.Kind, img.Size, img.MTime,
			img.GPSLat, img.GPSLon, boolInt(img.HasGPS), id)
		return id, false, err
	}
	if err != sql.ErrNoRows {
		return "", false, err
	}
	_, err = s.db.Exec(`
		INSERT INTO images (id, source_id, rel_path, name, sha256, phash, created_at,
		                      exif_capture, exif_offset, exif_camera, width, height, kind, size, mtime,
		                      gps_lat, gps_lon, has_gps, group_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		img.ID, img.SourceID, img.RelPath, img.Name, img.SHA256, img.Phash, img.CreatedAt,
		img.ExifCapture, img.ExifOffset, img.ExifCamera, img.Width, img.Height, img.Kind, img.Size, img.MTime,
		img.GPSLat, img.GPSLon, boolInt(img.HasGPS), img.GroupID)
	return img.ID, true, err
}

// AllImages returns every recorded image.
func (s *Store) AllImages() ([]Image, error) {
	rows, err := s.db.Query(`
		SELECT id, source_id, rel_path, name, sha256, phash, created_at,
		       exif_capture, exif_offset, exif_camera, width, height, kind, size, mtime,
		       gps_lat, gps_lon, has_gps, group_id
		FROM images ORDER BY source_id, rel_path`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Image
	for rows.Next() {
		var img Image
		var hasGPS int
		if err := rows.Scan(&img.ID, &img.SourceID, &img.RelPath, &img.Name, &img.SHA256,
			&img.Phash, &img.CreatedAt, &img.ExifCapture, &img.ExifOffset, &img.ExifCamera,
			&img.Width, &img.Height, &img.Kind, &img.Size, &img.MTime,
			&img.GPSLat, &img.GPSLon, &hasGPS, &img.GroupID); err != nil {
			return nil, err
		}
		img.HasGPS = hasGPS != 0
		out = append(out, img)
	}
	return out, rows.Err()
}

// ImagesBySource returns the stored rows of one source keyed by relative
// path. The ingest fingerprint cache uses them to skip re-hashing files
// whose (size, mtime) has not changed since the last scan.
func (s *Store) ImagesBySource(sourceID int64) (map[string]Image, error) {
	rows, err := s.db.Query(`
		SELECT id, source_id, rel_path, name, sha256, phash, created_at,
		       exif_capture, exif_offset, exif_camera, width, height, kind, size, mtime,
		       gps_lat, gps_lon, has_gps, group_id
		FROM images WHERE source_id = ?`, sourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Image{}
	for rows.Next() {
		var img Image
		var hasGPS int
		if err := rows.Scan(&img.ID, &img.SourceID, &img.RelPath, &img.Name, &img.SHA256,
			&img.Phash, &img.CreatedAt, &img.ExifCapture, &img.ExifOffset, &img.ExifCamera,
			&img.Width, &img.Height, &img.Kind, &img.Size, &img.MTime,
			&img.GPSLat, &img.GPSLon, &hasGPS, &img.GroupID); err != nil {
			return nil, err
		}
		img.HasGPS = hasGPS != 0
		out[img.RelPath] = img
	}
	return out, rows.Err()
}

// ClearGroups drops all group assignments and plans.
func (s *Store) ClearGroups() error {
	if _, err := s.db.Exec(`UPDATE images SET group_id = 0`); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM groups`)
	return err
}

// SetGroupAssignments stores imageID -> groupID for every grouped image.
func (s *Store) SetGroupAssignments(assign map[string]int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for id, groupID := range assign {
		if _, err := tx.Exec(`UPDATE images SET group_id = ? WHERE id = ?`, groupID, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// SaveGroup upserts the plan for one group.
func (s *Store) SaveGroup(g Group) error {
	_, err := s.db.Exec(`
		INSERT INTO groups (id, status, reason, resolved_by, pixel_image_id, pixel_phash, capture_image_id,
		                     capture_time, capture_instant, capture_fallback,
		                     gps_lat, gps_lon, has_gps, stored_name)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
		  status = excluded.status,
		  reason = excluded.reason,
		  resolved_by = excluded.resolved_by,
		  pixel_image_id = excluded.pixel_image_id,
		  pixel_phash = excluded.pixel_phash,
		  capture_image_id = excluded.capture_image_id,
		  capture_time = excluded.capture_time,
		  capture_instant = excluded.capture_instant,
		  capture_fallback = excluded.capture_fallback,
		  gps_lat = excluded.gps_lat,
		  gps_lon = excluded.gps_lon,
		  has_gps = excluded.has_gps,
		  stored_name = excluded.stored_name`,
		g.ID, g.Status, g.Reason, g.ResolvedBy, g.PixelImageID, g.PixelPhash, g.CaptureImageID,
		g.CaptureTime, g.CaptureInstant, boolInt(g.CaptureFallback),
		g.GPSLat, g.GPSLon, boolInt(g.HasGPS), g.StoredName)
	return err
}

// AllGroups returns every stored plan.
func (s *Store) AllGroups() ([]Group, error) {
	rows, err := s.db.Query(`
		SELECT id, status, reason, resolved_by, pixel_image_id, pixel_phash, capture_image_id,
		       capture_time, capture_instant, capture_fallback,
		       gps_lat, gps_lon, has_gps, stored_name
		FROM groups ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Group
	for rows.Next() {
		var g Group
		var fallback, hasGPS int
		if err := rows.Scan(&g.ID, &g.Status, &g.Reason, &g.ResolvedBy, &g.PixelImageID, &g.PixelPhash, &g.CaptureImageID,
			&g.CaptureTime, &g.CaptureInstant, &fallback,
			&g.GPSLat, &g.GPSLon, &hasGPS, &g.StoredName); err != nil {
			return nil, err
		}
		g.CaptureFallback = fallback != 0
		g.HasGPS = hasGPS != 0
		out = append(out, g)
	}
	return out, rows.Err()
}

// GroupCounts returns the number of groups per status.
func (s *Store) GroupCounts() (map[string]int, error) {
	rows, err := s.db.Query(`SELECT status, COUNT(*) FROM groups GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			return nil, err
		}
		out[status] = n
	}
	return out, rows.Err()
}

// GroupOutput is one row of the apply ledger: the file that apply last
// wrote into a target directory for one group, together with fingerprints
// of the pixel source and the merged EXIF facts at write time. An empty
// SHA256 or Facts means "unknown" (a row adopted from an older
// report.json): such rows are treated as current until the plan itself
// moves or renames the output.
type GroupOutput struct {
	Name      string // file name inside the target directory
	PixelID   string // pixel image the output was written from
	SHA256    string // sha256 of the pixel source at write time
	Facts     string // signature of the merged EXIF facts written
	Conflict  string // non-empty for conflict groups: why the plan is best effort
	Prefixed  bool   // the name differs from the stored name (collision prefix)
	WrittenAt int64  // unix seconds of the write
}

// GroupOutputs returns the apply ledger of one target directory keyed by
// group id.
func (s *Store) GroupOutputs(target string) (map[int64]GroupOutput, error) {
	rows, err := s.db.Query(`
		SELECT group_id, name, pixel_image_id, sha256, facts, conflict, prefixed, written_at
		FROM group_outputs WHERE target = ?`, target)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]GroupOutput{}
	for rows.Next() {
		var (
			gid      int64
			row      GroupOutput
			prefixed int
		)
		if err := rows.Scan(&gid, &row.Name, &row.PixelID, &row.SHA256, &row.Facts, &row.Conflict, &prefixed, &row.WrittenAt); err != nil {
			return nil, err
		}
		row.Prefixed = prefixed != 0
		out[gid] = row
	}
	return out, rows.Err()
}

// SaveGroupOutput records (or replaces) the ledger row of one group in one
// target directory.
func (s *Store) SaveGroupOutput(target string, groupID int64, o GroupOutput) error {
	_, err := s.db.Exec(`
		INSERT INTO group_outputs (target, group_id, pixel_image_id, name, sha256, facts, conflict, prefixed, written_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(target, group_id) DO UPDATE SET
		  pixel_image_id = excluded.pixel_image_id,
		  name = excluded.name,
		  sha256 = excluded.sha256,
		  facts = excluded.facts,
		  conflict = excluded.conflict,
		  prefixed = excluded.prefixed,
		  written_at = excluded.written_at`,
		target, groupID, o.PixelID, o.Name, o.SHA256, o.Facts, o.Conflict, boolInt(o.Prefixed), o.WrittenAt)
	return err
}

// DeleteGroupOutput drops the ledger row of one group in one target
// directory (used when a row moves to a renumbered group id).
func (s *Store) DeleteGroupOutput(target string, groupID int64) error {
	_, err := s.db.Exec(`DELETE FROM group_outputs WHERE target = ? AND group_id = ?`, target, groupID)
	return err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
