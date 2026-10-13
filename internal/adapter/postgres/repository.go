package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"uuid"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/arielcr/medication-api/internal/medication"
)

// uniqueViolation is the PostgreSQL error code (SQLSTATE) for a unique
// constraint violation.
const uniqueViolation = "23505"

const selectColumns = `id, name, dosage, form, version, created_at, updated_at`

// Repository is a medication.Repository backed by PostgreSQL.
//
// Every query uses placeholders ($1, $2, ...) and never concatenates user
// input into SQL, which prevents SQL injection.
type Repository struct {
	pool *pgxpool.Pool
}

// Compile-time check that *Repository implements medication.Repository.
var _ medication.Repository = (*Repository)(nil)

// NewRepository returns a repository that uses pool.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// Create implements medication.Repository.
func (r *Repository) Create(ctx context.Context, m medication.Medication) error {
	const q = `
		INSERT INTO medications (id, name, dosage, form, version, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`

	_, err := r.pool.Exec(ctx, q, m.ID, m.Name, m.Dosage, string(m.Form), m.Version, m.CreatedAt, m.UpdatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return medication.ErrAlreadyExists
		}
		return fmt.Errorf("insert medication: %w", err)
	}

	return nil
}

// Get implements medication.Repository.
func (r *Repository) Get(ctx context.Context, id uuid.UUID) (medication.Medication, error) {
	q := `SELECT ` + selectColumns + ` FROM medications WHERE id = $1`

	m, err := scanMedication(r.pool.QueryRow(ctx, q, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return medication.Medication{}, medication.ErrNotFound
		}
		return medication.Medication{}, fmt.Errorf("select medication: %w", err)
	}

	return m, nil
}

// List implements medication.Repository using keyset pagination: instead
// of OFFSET (which makes the database read and discard every skipped
// row), it asks for rows "after" the last ID seen, which the primary key
// index answers directly no matter how deep the page is.
//
// Optional filters use the pattern below, so a single static query covers
// every combination of filters with no SQL string building:
//
//	($2 = '' OR form = $2)
func (r *Repository) List(ctx context.Context, filter medication.ListFilter) ([]medication.Medication, error) {
	q := `
		SELECT ` + selectColumns + `
		FROM medications
		WHERE id > $1
		  AND ($2 = '' OR form = $2)
		  AND ($3 = '' OR name ILIKE '%' || $3 || '%' ESCAPE '\')
		ORDER BY id
		LIMIT $4`

	rows, err := r.pool.Query(ctx, q, filter.After, string(filter.Form), escapeLike(filter.Name), filter.Limit)
	if err != nil {
		return nil, fmt.Errorf("list medications: %w", err)
	}
	defer rows.Close()

	items := make([]medication.Medication, 0, filter.Limit)
	for rows.Next() {
		m, err := scanMedication(rows)
		if err != nil {
			return nil, fmt.Errorf("scan medication: %w", err)
		}
		items = append(items, m)
	}
	// rows.Err reports errors that happened while iterating (e.g. the
	// connection dropped halfway). Forgetting it hides partial results.
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate medications: %w", err)
	}

	return items, nil
}

// Update implements medication.Repository with optimistic locking: the
// WHERE clause only matches if nobody changed the row since we read it.
// The check and the write happen in one atomic statement, so there is no
// window for a race condition.
func (r *Repository) Update(ctx context.Context, m medication.Medication) error {
	const q = `
		UPDATE medications
		SET name = $2, dosage = $3, form = $4, updated_at = $5, version = version + 1
		WHERE id = $1 AND version = $6`

	tag, err := r.pool.Exec(ctx, q, m.ID, m.Name, m.Dosage, string(m.Form), m.UpdatedAt, m.Version)
	if err != nil {
		if isUniqueViolation(err) {
			return medication.ErrAlreadyExists
		}
		return fmt.Errorf("update medication: %w", err)
	}

	if tag.RowsAffected() == 0 {
		// Nothing matched: either the row is gone or its version moved on.
		// One extra query tells the two apart so the client gets 404 vs 412.
		var exists bool
		if err := r.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM medications WHERE id = $1)`, m.ID).Scan(&exists); err != nil {
			return fmt.Errorf("check medication exists: %w", err)
		}
		if !exists {
			return medication.ErrNotFound
		}
		return medication.ErrVersionMismatch
	}

	return nil
}

// Delete implements medication.Repository. A single DELETE that checks
// the affected row count avoids the "read, then delete" race.
func (r *Repository) Delete(ctx context.Context, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM medications WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete medication: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return medication.ErrNotFound
	}

	return nil
}

// scanMedication reads one row. pgx.Row is satisfied by both the result
// of QueryRow and by pgx.Rows, so Get and List share this code.
func scanMedication(row pgx.Row) (medication.Medication, error) {
	var m medication.Medication
	var form string

	if err := row.Scan(&m.ID, &m.Name, &m.Dosage, &form, &m.Version, &m.CreatedAt, &m.UpdatedAt); err != nil {
		return medication.Medication{}, err
	}

	m.Form = medication.Form(form)
	// pgx returns timestamps in the local time zone; normalize to UTC so
	// every repository returns identical values.
	m.CreatedAt = m.CreatedAt.UTC()
	m.UpdatedAt = m.UpdatedAt.UTC()

	return m, nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == uniqueViolation
}

// likeEscaper escapes the LIKE wildcards so user input is matched
// literally: searching for "100%" must not mean "100 followed by
// anything".
var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

func escapeLike(s string) string {
	return likeEscaper.Replace(s)
}
