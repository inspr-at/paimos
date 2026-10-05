// SPDX-License-Identifier: AGPL-3.0-only

package delivery

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const neighbourReadTimeout = 15 * time.Second

// Container is explicitly tenant/project bound. Empty ReleaseID is backlog.
type Container struct {
	TenantID  string
	ProjectID string
	ReleaseID string
}

type Neighbour struct {
	ID   string
	Rank string
}

type Neighbours struct {
	Previous *Neighbour
	Next     *Neighbour
}

func uuid(id string) bool {
	if len(id) != 36 {
		return false
	}
	var value pgtype.UUID
	return value.Scan(id) == nil && value.Valid
}

func validateNeighbours(c Container, anchor, excludeID string) error {
	if !uuid(c.TenantID) || !uuid(c.ProjectID) || c.ReleaseID != "" && !uuid(c.ReleaseID) || excludeID != "" && !uuid(excludeID) {
		return errors.New("neighbour queries require UUID identities")
	}
	if anchor != "" && !ValidRank(anchor) {
		return ErrInvalidRank
	}
	return nil
}

const liveProject = `EXISTS (
 SELECT 1 FROM nodes p JOIN node_kinds k ON k.tenant_id=p.tenant_id AND k.id=p.kind_id
 WHERE p.tenant_id=$1::uuid AND p.id=$2::uuid AND p.deleted_at IS NULL AND k.slug='project'
)`

// neighbourQuery uses only fixed table/column choices. NULL backlog gets its
// own predicate so the unique rank index can bound each direction directly.
func neighbourQuery(c Container, anchor, excludeID string, items, previous bool) (string, []any) {
	table, id := "project_releases", "release_node_id"
	if items {
		table, id = "ships_in", "item_node_id"
	}
	args := []any{c.TenantID, c.ProjectID}
	where := `tenant_id=$1::uuid AND project_node_id=$2::uuid AND ` + liveProject
	if items {
		if c.ReleaseID == "" {
			where += ` AND release_node_id IS NULL`
		} else {
			args = append(args, c.ReleaseID)
			where += ` AND release_node_id=$3::uuid`
		}
	}
	op, order := ">", "ASC"
	if previous {
		op, order = "<", "DESC"
	}
	if anchor != "" {
		args = append(args, anchor)
		where += ` AND rank` + op + `$` + strconv.Itoa(len(args)) + `::text COLLATE "C"`
	}
	if excludeID != "" {
		args = append(args, excludeID)
		where += ` AND ` + id + `<>$` + strconv.Itoa(len(args)) + `::uuid`
	}
	sort := `rank ` + order
	if items {
		// IS NULL is not an equality pathkey to PostgreSQL. Retain the
		// fixed container column in ORDER BY, matching the complete index
		// suffix, so backlog seeks do not scan/sort the whole slice.
		sort = `release_node_id ` + order + `, ` + sort
	}
	return `SELECT ` + id + `::text,rank FROM ` + table + ` WHERE ` + where + ` ORDER BY ` + sort + ` LIMIT 1`, args
}

func readNeighbour(ctx context.Context, tx pgx.Tx, sql string, args []any) (*Neighbour, error) {
	var out Neighbour
	err := tx.QueryRow(ctx, sql, args...).Scan(&out.ID, &out.Rank)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func neighbours(ctx context.Context, tx pgx.Tx, c Container, anchor, excludeID string, items bool) (Neighbours, error) {
	var out Neighbours
	if err := validateNeighbours(c, anchor, excludeID); err != nil {
		return out, err
	}
	ctx, cancel := context.WithTimeout(ctx, neighbourReadTimeout)
	defer cancel()
	q, args := neighbourQuery(c, anchor, excludeID, items, true)
	var err error
	out.Previous, err = readNeighbour(ctx, tx, q, args)
	if err != nil {
		return Neighbours{}, err
	}
	q, args = neighbourQuery(c, anchor, excludeID, items, false)
	out.Next, err = readNeighbour(ctx, tx, q, args)
	if err != nil {
		return Neighbours{}, err
	}
	return out, nil
}

// ItemNeighbours returns at most two ranked rows around anchor, excluding the
// moved item. Empty anchor selects the last and first rows of the container.
// It retains tombstones/hidden rows so their occupied keys cannot be reused.
// The transaction's RLS/visibility apply; a writer must hold its project
// fence before calling and through the final write to keep neighbours stable.
func ItemNeighbours(ctx context.Context, tx pgx.Tx, c Container, anchor, excludeID string) (Neighbours, error) {
	return neighbours(ctx, tx, c, anchor, excludeID, true)
}

// ReleaseNeighbours uses the same two bounded seeks for project release rank.
func ReleaseNeighbours(ctx context.Context, tx pgx.Tx, tenantID, projectID, anchor, excludeID string) (Neighbours, error) {
	return neighbours(ctx, tx, Container{TenantID: tenantID, ProjectID: projectID}, anchor, excludeID, false)
}

// PublishedBounds reads the nearest lower and higher published sequences in
// any state. It supports reranking and conversion without loading the project.
// Call CheckPublishedRank with the candidate rank; this read grants no write.
func PublishedBounds(ctx context.Context, tx pgx.Tx, tenantID, projectID string, sequence int) (lower, higher *NumberedRank, err error) {
	if !uuid(tenantID) || !uuid(projectID) || sequence < 1 {
		return nil, nil, errors.New("published bounds need UUID identities and a positive sequence")
	}
	ctx, cancel := context.WithTimeout(ctx, neighbourReadTimeout)
	defer cancel()
	read := func(op, order string) (*NumberedRank, error) {
		var out NumberedRank
		q := fmt.Sprintf(`SELECT sequence,rank FROM project_releases
 WHERE tenant_id=$1::uuid AND project_node_id=$2::uuid AND visibility='published'
 AND %s AND sequence%s$3 ORDER BY sequence %s LIMIT 1`, liveProject, op, order)
		err := tx.QueryRow(ctx, q, tenantID, projectID, sequence).Scan(&out.Sequence, &out.Rank)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		return &out, nil
	}
	lower, err = read("<", "DESC")
	if err != nil {
		return nil, nil, err
	}
	higher, err = read(">", "ASC")
	if err != nil {
		return nil, nil, err
	}
	return lower, higher, nil
}
