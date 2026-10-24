package store

import (
	"database/sql"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Per-group actions between two commits.
const (
	ActionNew     = "new"
	ActionUpdated = "updated"
	ActionRemoved = "removed"
)

// Commit is one entry in the commit log: a timestamped snapshot of the plan
// table (ch17 commit model, adapted to the current groups schema).
type Commit struct {
	ID      int64
	At      int64 // unix seconds
	Summary string
}

// CommitAction is the diff of one group between the previous commit and
// this one.
type CommitAction struct {
	GroupID  int64
	Action   string // new | updated | removed
	PrevName string // stored_name before the commit
	NewName  string // stored_name after the commit
}

// CommitResult is a new commit together with its per-group diff.
type CommitResult struct {
	Commit  Commit
	Actions []CommitAction
}

// Commit snapshots the current plan table into the commit log in one
// transaction: the commit row, the full group snapshot, and the per-group
// actions versus the previous commit. The working plans in groups keep
// evolving; only snapshots are committed state.
func (s *Store) Commit(summary string) (CommitResult, error) {
	cur, err := s.AllGroups()
	if err != nil {
		return CommitResult{}, err
	}
	prev, err := s.latestSnapshot()
	if err != nil {
		return CommitResult{}, err
	}
	res := CommitResult{Commit: Commit{Summary: summary}, Actions: groupDiff(curByID(cur), prev)}
	if err := s.insertCommit(&res, cur); err != nil {
		return CommitResult{}, err
	}
	return res, nil
}

// Rollback restores the plan table to the snapshot stored in commitID, in
// one transaction: the groups rows are replaced, image group assignments
// that no longer point at a group are zeroed, and the restore itself is
// recorded as a new commit ("rollback to commit N"), keeping the log
// append-only. Scanned images are untouched; re-running the plan phase
// re-assigns memberships.
func (s *Store) Rollback(commitID int64) (CommitResult, error) {
	snap, ok, err := s.snapshot(commitID)
	if err != nil {
		return CommitResult{}, err
	}
	if !ok {
		return CommitResult{}, fmt.Errorf("commit %d not found", commitID)
	}
	cur, err := s.AllGroups()
	if err != nil {
		return CommitResult{}, err
	}
	res := CommitResult{
		Commit:  Commit{Summary: fmt.Sprintf("rollback to commit %d", commitID)},
		Actions: groupDiff(snap, curByID(cur)),
	}
	tx, err := s.db.Begin()
	if err != nil {
		return CommitResult{}, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM groups`); err != nil {
		return CommitResult{}, err
	}
	groups := snapshotList(snap)
	for _, g := range groups {
		if _, err := tx.Exec(`
			INSERT INTO groups (id, status, reason, resolved_by, pixel_image_id, pixel_phash,
			                    capture_image_id, capture_time, capture_instant, capture_fallback,
			                    gps_lat, gps_lon, has_gps, stored_name)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			g.ID, g.Status, g.Reason, g.ResolvedBy,
			g.PixelImageID, g.PixelPhash, g.CaptureImageID,
			g.CaptureTime, g.CaptureInstant, boolInt(g.CaptureFallback),
			g.GPSLat, g.GPSLon, boolInt(g.HasGPS), g.StoredName); err != nil {
			return CommitResult{}, err
		}
	}
	if err := s.zeroDanglingAssignments(tx, groups); err != nil {
		return CommitResult{}, err
	}
	if err := insertCommitTx(tx, &res); err != nil {
		return CommitResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CommitResult{}, err
	}
	return res, nil
}

// Commits returns the commit log in ascending id order.
func (s *Store) Commits() ([]Commit, error) {
	rows, err := s.db.Query(`SELECT id, at, summary FROM commits ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Commit
	for rows.Next() {
		var c Commit
		if err := rows.Scan(&c.ID, &c.At, &c.Summary); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// CommitActions returns the per-group diff of one commit.
func (s *Store) CommitActions(commitID int64) ([]CommitAction, error) {
	rows, err := s.db.Query(`
		SELECT group_id, action, prev_stored_name, new_stored_name
		FROM commit_actions WHERE commit_id = ? ORDER BY group_id`, commitID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CommitAction
	for rows.Next() {
		var a CommitAction
		if err := rows.Scan(&a.GroupID, &a.Action, &a.PrevName, &a.NewName); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// insertCommit records one commit (row, snapshot, actions) on its own
// transaction and fills in the assigned commit id and timestamp.
func (s *Store) insertCommit(res *CommitResult, groups []Group) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := insertCommitTx(tx, res); err != nil {
		return err
	}
	for _, g := range groups {
		if _, err := tx.Exec(`
			INSERT INTO commit_groups (commit_id, group_id, status, reason, resolved_by,
			                          pixel_image_id, pixel_phash, capture_image_id,
			                          capture_time, capture_instant, capture_fallback,
			                          gps_lat, gps_lon, has_gps, stored_name)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			res.Commit.ID, g.ID, g.Status, g.Reason, g.ResolvedBy,
			g.PixelImageID, g.PixelPhash, g.CaptureImageID,
			g.CaptureTime, g.CaptureInstant, boolInt(g.CaptureFallback),
			g.GPSLat, g.GPSLon, boolInt(g.HasGPS), g.StoredName); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// insertCommitTx writes the commit row and its actions into an open
// transaction and fills in the assigned commit id and timestamp.
func insertCommitTx(tx *sql.Tx, res *CommitResult) error {
	now := time.Now().Unix()
	out, err := tx.Exec(`INSERT INTO commits (at, summary) VALUES (?, ?)`, now, res.Commit.Summary)
	if err != nil {
		return err
	}
	id, err := out.LastInsertId()
	if err != nil {
		return err
	}
	res.Commit = Commit{ID: id, At: now, Summary: res.Commit.Summary}
	for _, a := range res.Actions {
		if _, err := tx.Exec(`
			INSERT INTO commit_actions (commit_id, group_id, action, prev_stored_name, new_stored_name)
			VALUES (?, ?, ?, ?, ?)`,
			id, a.GroupID, a.Action, a.PrevName, a.NewName); err != nil {
			return err
		}
	}
	return nil
}

// latestSnapshot returns the group snapshot of the most recent commit, or
// an empty map when the log is empty.
func (s *Store) latestSnapshot() (map[int64]Group, error) {
	var id sql.NullInt64
	if err := s.db.QueryRow(`SELECT MAX(id) FROM commits`).Scan(&id); err != nil {
		return nil, err
	}
	if !id.Valid {
		return map[int64]Group{}, nil
	}
	snap, _, err := s.snapshot(id.Int64)
	return snap, err
}

// snapshot loads one commit's group snapshot. The boolean reports whether
// the commit exists (a commit with zero groups is valid).
func (s *Store) snapshot(commitID int64) (map[int64]Group, bool, error) {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM commits WHERE id = ?`, commitID).Scan(&n); err != nil {
		return nil, false, err
	}
	if n == 0 {
		return nil, false, nil
	}
	rows, err := s.db.Query(`
		SELECT group_id, status, reason, resolved_by, pixel_image_id, pixel_phash, capture_image_id,
		       capture_time, capture_instant, capture_fallback,
		       gps_lat, gps_lon, has_gps, stored_name
		FROM commit_groups WHERE commit_id = ?`, commitID)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	out := map[int64]Group{}
	for rows.Next() {
		var g Group
		var fallback, hasGPS int
		if err := rows.Scan(&g.ID, &g.Status, &g.Reason, &g.ResolvedBy, &g.PixelImageID, &g.PixelPhash, &g.CaptureImageID,
			&g.CaptureTime, &g.CaptureInstant, &fallback,
			&g.GPSLat, &g.GPSLon, &hasGPS, &g.StoredName); err != nil {
			return nil, false, err
		}
		g.CaptureFallback = fallback != 0
		g.HasGPS = hasGPS != 0
		out[g.ID] = g
	}
	return out, true, rows.Err()
}

// zeroDanglingAssignments clears images.group_id for images that no longer
// point at a surviving group.
func (s *Store) zeroDanglingAssignments(tx *sql.Tx, groups []Group) error {
	if len(groups) == 0 {
		_, err := tx.Exec(`UPDATE images SET group_id = 0`)
		return err
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(groups)), ",")
	args := make([]any, len(groups))
	for i, g := range groups {
		args[i] = g.ID
	}
	_, err := tx.Exec(`UPDATE images SET group_id = 0 WHERE group_id NOT IN (`+ph+`)`, args...)
	return err
}

func curByID(groups []Group) map[int64]Group {
	out := make(map[int64]Group, len(groups))
	for _, g := range groups {
		out[g.ID] = g
	}
	return out
}

// groupDiff reports the per-group changes of cur relative to prev, in
// group id order: groups that appear, change, or disappear.
func groupDiff(cur, prev map[int64]Group) []CommitAction {
	var out []CommitAction
	for _, id := range sortedGroupIDs(cur) {
		old, ok := prev[id]
		switch {
		case !ok:
			out = append(out, CommitAction{GroupID: id, Action: ActionNew, NewName: cur[id].StoredName})
		case !groupEqual(old, cur[id]):
			out = append(out, CommitAction{GroupID: id, Action: ActionUpdated, PrevName: old.StoredName, NewName: cur[id].StoredName})
		}
	}
	for _, id := range sortedGroupIDs(prev) {
		if _, ok := cur[id]; !ok {
			out = append(out, CommitAction{GroupID: id, Action: ActionRemoved, PrevName: prev[id].StoredName})
		}
	}
	return out
}

// groupEqual compares two plans ignoring their id.
func groupEqual(a, b Group) bool {
	a.ID, b.ID = 0, 0
	return a == b
}

func sortedGroupIDs(m map[int64]Group) []int64 {
	ids := make([]int64, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

func snapshotList(snap map[int64]Group) []Group {
	out := make([]Group, 0, len(snap))
	for _, id := range sortedGroupIDs(snap) {
		out = append(out, snap[id])
	}
	return out
}
