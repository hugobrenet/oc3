package cdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

type (
	// AIContent is a resource or a skill of the AI assistant, as stored: the
	// MCP servers read them for the agent, an AIManager writes them. Title,
	// Description and MimeType are the resource's; Frontmatter, its JSON, the
	// skill's.
	AIContent struct {
		Name         string
		Title        string
		Description  string
		MimeType     string
		Frontmatter  string
		Backends     []string
		Publications []int
		Body         string
		Size         int
		Digest       string
		Updated      time.Time
		UpdatedBy    string
		// Reads are the reads of the assistants, by backend; read with
		// AIContentFilter.WithReads only.
		Reads []AIRead
	}

	// AIRead counts the reads of an object by the assistants of a backend:
	// the reads of its text with that backend, the last at Last.
	AIRead struct {
		Backend string
		Count   int
		Last    time.Time
	}

	// AIContentKind is one of the two collections, a table and the table of
	// the groups each object is published to.
	AIContentKind struct {
		table, publications, reads, idColumn string
		skill                                bool
	}

	// AIContentFilter selects the objects a read gives: of a backend when
	// Backend is set, the one named when Name is set, those published to one
	// of Groups or to none unless All.
	AIContentFilter struct {
		Backend  string
		Name     string
		Groups   []string
		All      bool
		WithBody bool
		// WithReads reads the reads of each object too, for an AIManager.
		WithReads bool
	}
)

var (
	AIResources = AIContentKind{table: "ai_resources", publications: "ai_resources_publications", reads: "ai_resources_reads", idColumn: "resource_id"}
	AISkills    = AIContentKind{table: "ai_skills", publications: "ai_skills_publications", reads: "ai_skills_reads", idColumn: "skill_id", skill: true}
)

// aiVisibility restricts the objects of kind to those published to none of
// the groups, or to one of groups.
func aiVisibility(kind AIContentKind, groups []string) (string, []any) {
	cond := "NOT EXISTS (SELECT 1 FROM " + kind.publications + " p WHERE p." + kind.idColumn + " = o.id)"
	if len(groups) == 0 {
		return cond, nil
	}
	args := make([]any, len(groups))
	for i, g := range groups {
		args[i] = g
	}
	cond = "(" + cond + " OR o.id IN (SELECT p." + kind.idColumn + " FROM " + kind.publications + " p" +
		" JOIN auth_group ag ON ag.id = p.group_id WHERE ag.role IN (" + Placeholders(len(groups)) + ")))"
	return cond, args
}

// AIContents reads the objects of kind the filter selects, sorted by name.
func (oDb *DB) AIContents(ctx context.Context, kind AIContentKind, f AIContentFilter) ([]AIContent, error) {
	columns := "o.id, o.name, o.backends, o.size, o.digest, o.updated"
	if kind.skill {
		columns += ", o.frontmatter"
	} else {
		columns += ", o.title, o.description, o.mime_type"
	}
	if f.WithBody {
		columns += ", o.body"
	}
	var conds []string
	var args []any
	if f.Backend != "" {
		conds = append(conds, "FIND_IN_SET(?, o.backends) > 0")
		args = append(args, f.Backend)
	}
	if f.Name != "" {
		conds = append(conds, "o.name = ?")
		args = append(args, f.Name)
	}
	if !f.All {
		cond, condArgs := aiVisibility(kind, f.Groups)
		conds = append(conds, cond)
		args = append(args, condArgs...)
	}
	query := "SELECT " + columns + " FROM " + kind.table + " o"
	if len(conds) > 0 {
		query += " WHERE " + strings.Join(conds, " AND ")
	}
	query += " ORDER BY o.name"
	rows, err := oDb.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("aiContents %s: %w", kind.table, err)
	}
	defer func() { _ = rows.Close() }()
	var (
		items []AIContent
		ids   []any
		index = map[int64]int{}
	)
	for rows.Next() {
		var (
			id       int64
			c        AIContent
			backends string
		)
		dest := []any{&id, &c.Name, &backends, &c.Size, &c.Digest, &c.Updated}
		if kind.skill {
			dest = append(dest, &c.Frontmatter)
		} else {
			dest = append(dest, &c.Title, &c.Description, &c.MimeType)
		}
		if f.WithBody {
			dest = append(dest, &c.Body)
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, fmt.Errorf("aiContents %s: %w", kind.table, err)
		}
		c.Backends = strings.Split(backends, ",")
		c.Publications = []int{}
		index[id] = len(items)
		ids = append(ids, id)
		items = append(items, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("aiContents %s: %w", kind.table, err)
	}
	if len(ids) == 0 {
		return items, nil
	}
	pubs, err := oDb.DB.QueryContext(ctx, "SELECT "+kind.idColumn+", group_id FROM "+kind.publications+
		" WHERE "+kind.idColumn+" IN ("+Placeholders(len(ids))+") ORDER BY group_id", ids...)
	if err != nil {
		return nil, fmt.Errorf("aiContents %s publications: %w", kind.table, err)
	}
	defer func() { _ = pubs.Close() }()
	for pubs.Next() {
		var id int64
		var group int
		if err := pubs.Scan(&id, &group); err != nil {
			return nil, fmt.Errorf("aiContents %s publications: %w", kind.table, err)
		}
		if i, ok := index[id]; ok {
			items[i].Publications = append(items[i].Publications, group)
		}
	}
	if err := pubs.Err(); err != nil {
		return nil, fmt.Errorf("aiContents %s publications: %w", kind.table, err)
	}
	if !f.WithReads {
		return items, nil
	}
	reads, err := oDb.DB.QueryContext(ctx, "SELECT "+kind.idColumn+", backend, read_count, last_read FROM "+kind.reads+
		" WHERE "+kind.idColumn+" IN ("+Placeholders(len(ids))+") ORDER BY backend", ids...)
	if err != nil {
		return nil, fmt.Errorf("aiContents %s reads: %w", kind.table, err)
	}
	defer func() { _ = reads.Close() }()
	for reads.Next() {
		var id int64
		var r AIRead
		if err := reads.Scan(&id, &r.Backend, &r.Count, &r.Last); err != nil {
			return nil, fmt.Errorf("aiContents %s reads: %w", kind.table, err)
		}
		if i, ok := index[id]; ok {
			items[i].Reads = append(items[i].Reads, r)
		}
	}
	return items, reads.Err()
}

// CountAIRead counts a read of the object of kind named name by the
// assistants of backend. An object deleted meanwhile counts nothing. It does
// not mark the table changed: a read changes nothing the views follow.
func (oDb *DB) CountAIRead(ctx context.Context, kind AIContentKind, name, backend string) error {
	_, err := oDb.DB.ExecContext(ctx, "INSERT INTO "+kind.reads+" ("+kind.idColumn+", backend, read_count, last_read)"+
		" SELECT id, ?, 1, NOW() FROM "+kind.table+" WHERE name = ?"+
		" ON DUPLICATE KEY UPDATE read_count = read_count + 1, last_read = NOW()", backend, name)
	if err != nil {
		return fmt.Errorf("countAIRead %s: %w", kind.table, err)
	}
	return nil
}

// AIContentCount counts the objects of kind for backend, but the one named
// except: a write replacing it counts the others.
func (oDb *DB) AIContentCount(ctx context.Context, kind AIContentKind, backend, except string) (int, error) {
	var n int
	err := oDb.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+kind.table+" WHERE FIND_IN_SET(?, backends) > 0 AND name <> ?", backend, except).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("aiContentCount %s: %w", kind.table, err)
	}
	return n, nil
}

// AIContentID gives the id of the object of kind named name; found false
// when there is none.
func (oDb *DB) AIContentID(ctx context.Context, kind AIContentKind, name string) (int64, bool, error) {
	var id int64
	err := oDb.DB.QueryRowContext(ctx, "SELECT id FROM "+kind.table+" WHERE name = ?", name).Scan(&id)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return 0, false, nil
	case err != nil:
		return 0, false, fmt.Errorf("aiContentID %s: %w", kind.table, err)
	}
	return id, true, nil
}

// MissingGroups gives the ids of groups that name no group.
func (oDb *DB) MissingGroups(ctx context.Context, groups []int) ([]int, error) {
	if len(groups) == 0 {
		return nil, nil
	}
	args := make([]any, len(groups))
	for i, g := range groups {
		args[i] = g
	}
	rows, err := oDb.DB.QueryContext(ctx, "SELECT id FROM auth_group WHERE id IN ("+Placeholders(len(groups))+")", args...)
	if err != nil {
		return nil, fmt.Errorf("missingGroups: %w", err)
	}
	defer func() { _ = rows.Close() }()
	found := map[int]bool{}
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("missingGroups: %w", err)
		}
		found[id] = true
	}
	var missing []int
	for _, g := range groups {
		if !found[g] {
			missing = append(missing, g)
		}
	}
	return missing, rows.Err()
}

// InsertAIContent creates an object of kind, with its publications; call it
// on a transaction, so that the object is never visible without them. It
// gives ErrDuplicate when the name is taken.
func (oDb *DB) InsertAIContent(ctx context.Context, kind AIContentKind, c AIContent) error {
	var (
		result sql.Result
		err    error
	)
	if kind.skill {
		result, err = oDb.DB.ExecContext(ctx, "INSERT INTO "+kind.table+
			" (name, backends, frontmatter, body, size, digest, updated, updated_by) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
			c.Name, strings.Join(c.Backends, ","), c.Frontmatter, c.Body, c.Size, c.Digest, c.Updated, c.UpdatedBy)
	} else {
		result, err = oDb.DB.ExecContext(ctx, "INSERT INTO "+kind.table+
			" (name, title, description, mime_type, backends, body, size, digest, updated, updated_by) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
			c.Name, c.Title, c.Description, c.MimeType, strings.Join(c.Backends, ","), c.Body, c.Size, c.Digest, c.Updated, c.UpdatedBy)
	}
	if isDuplicate(err) {
		return ErrDuplicate
	}
	if err != nil {
		return fmt.Errorf("insertAIContent %s: %w", kind.table, err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return fmt.Errorf("insertAIContent %s: %w", kind.table, err)
	}
	oDb.SetChange(kind.table)
	return oDb.insertAIPublications(ctx, kind, id, c.Publications)
}

// UpdateAIContent replaces the writable fields of the object of kind named
// name, and its publications; found false when there is none. Call it on a
// transaction, so that no read sees the object between the deletion of its
// publications and their insertion, published to everybody.
func (oDb *DB) UpdateAIContent(ctx context.Context, kind AIContentKind, name string, c AIContent) (bool, error) {
	id, found, err := oDb.AIContentID(ctx, kind, name)
	if err != nil || !found {
		return found, err
	}
	if kind.skill {
		_, err = oDb.DB.ExecContext(ctx, "UPDATE "+kind.table+
			" SET backends = ?, frontmatter = ?, body = ?, size = ?, digest = ?, updated = ?, updated_by = ? WHERE id = ?",
			strings.Join(c.Backends, ","), c.Frontmatter, c.Body, c.Size, c.Digest, c.Updated, c.UpdatedBy, id)
	} else {
		_, err = oDb.DB.ExecContext(ctx, "UPDATE "+kind.table+
			" SET title = ?, description = ?, mime_type = ?, backends = ?, body = ?, size = ?, digest = ?, updated = ?, updated_by = ? WHERE id = ?",
			c.Title, c.Description, c.MimeType, strings.Join(c.Backends, ","), c.Body, c.Size, c.Digest, c.Updated, c.UpdatedBy, id)
	}
	if err != nil {
		return true, fmt.Errorf("updateAIContent %s: %w", kind.table, err)
	}
	oDb.SetChange(kind.table)
	if err := oDb.deleteAIPublications(ctx, kind, id); err != nil {
		return true, err
	}
	return true, oDb.insertAIPublications(ctx, kind, id, c.Publications)
}

// DeleteAIContent deletes the object of kind named name, with its
// publications and its reads; found false when there is none. Call it on a transaction: the
// object goes before its publications, so that it is never published to
// everybody.
func (oDb *DB) DeleteAIContent(ctx context.Context, kind AIContentKind, name string) (bool, error) {
	id, found, err := oDb.AIContentID(ctx, kind, name)
	if err != nil || !found {
		return found, err
	}
	if _, err := oDb.DB.ExecContext(ctx, "DELETE FROM "+kind.table+" WHERE id = ?", id); err != nil {
		return true, fmt.Errorf("deleteAIContent %s: %w", kind.table, err)
	}
	oDb.SetChange(kind.table)
	if _, err := oDb.DB.ExecContext(ctx, "DELETE FROM "+kind.reads+" WHERE "+kind.idColumn+" = ?", id); err != nil {
		return true, fmt.Errorf("deleteAIContent %s reads: %w", kind.table, err)
	}
	return true, oDb.deleteAIPublications(ctx, kind, id)
}

func (oDb *DB) deleteAIPublications(ctx context.Context, kind AIContentKind, id int64) error {
	if _, err := oDb.DB.ExecContext(ctx, "DELETE FROM "+kind.publications+" WHERE "+kind.idColumn+" = ?", id); err != nil {
		return fmt.Errorf("deleteAIPublications %s: %w", kind.table, err)
	}
	oDb.SetChange(kind.publications)
	return nil
}

// insertAIPublications publishes the object id of kind to groups, in one
// statement.
func (oDb *DB) insertAIPublications(ctx context.Context, kind AIContentKind, id int64, groups []int) error {
	if len(groups) == 0 {
		return nil
	}
	values := make([]string, len(groups))
	args := make([]any, 0, 2*len(groups))
	for i, g := range groups {
		values[i] = "(?, ?)"
		args = append(args, id, g)
	}
	if _, err := oDb.DB.ExecContext(ctx, "INSERT INTO "+kind.publications+" ("+kind.idColumn+", group_id) VALUES "+strings.Join(values, ", "), args...); err != nil {
		return fmt.Errorf("insertAIPublications %s: %w", kind.table, err)
	}
	oDb.SetChange(kind.publications)
	return nil
}
