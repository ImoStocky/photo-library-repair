package store

import (
	"database/sql"
	"fmt"
)

// ContentPhash is one perceptual hash of a file's bytes, keyed by content
// digest so the same picture from different paths/sources shares one row.
type ContentPhash struct {
	SHA256 string
	Alg    string // phash.Alg string, e.g. "dct", "blockhash"
	Size   int    // 0 = legacy DCT 16×8; otherwise 8, 16, or 32
	Hex    string
}

// PutContentPhash upserts one content-addressed perceptual hash.
func (s *Store) PutContentPhash(h ContentPhash) error {
	if h.SHA256 == "" || h.Alg == "" || h.Hex == "" {
		return fmt.Errorf("content_phash: sha256, alg and hex are required")
	}
	_, err := s.db.Exec(`
		INSERT INTO content_phash (sha256, alg, size, hex) VALUES (?, ?, ?, ?)
		ON CONFLICT(sha256, alg, size) DO UPDATE SET hex = excluded.hex`,
		h.SHA256, h.Alg, h.Size, h.Hex)
	return err
}

// GetContentPhash returns the hex hash for (sha256, alg, size).
func (s *Store) GetContentPhash(sha256, alg string, size int) (hex string, ok bool, err error) {
	err = s.db.QueryRow(`
		SELECT hex FROM content_phash WHERE sha256 = ? AND alg = ? AND size = ?`,
		sha256, alg, size).Scan(&hex)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return hex, true, nil
}

// ContentPhashes returns every stored perceptual hash for one digest.
func (s *Store) ContentPhashes(sha256 string) ([]ContentPhash, error) {
	rows, err := s.db.Query(`
		SELECT sha256, alg, size, hex FROM content_phash WHERE sha256 = ? ORDER BY alg, size`,
		sha256)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ContentPhash
	for rows.Next() {
		var h ContentPhash
		if err := rows.Scan(&h.SHA256, &h.Alg, &h.Size, &h.Hex); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}
