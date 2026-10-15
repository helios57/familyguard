package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

const playSourceCols = `package_name, tracks, label, added_at, checked_at, published_version, last_error`

func scanPlaySource(row pgx.Row) (*PlaySource, error) {
	var p PlaySource
	if err := row.Scan(&p.PackageName, &p.Tracks, &p.Label, &p.AddedAt, &p.CheckedAt,
		&p.PublishedVersion, &p.LastError); err != nil {
		return nil, mapErr(err)
	}
	return &p, nil
}

// ListPlaySources returns every package the server follows on Google Play, by package name.
func (s *Store) ListPlaySources(ctx context.Context) ([]PlaySource, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+playSourceCols+` FROM play_sources ORDER BY package_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PlaySource{}
	for rows.Next() {
		p, err := scanPlaySource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// GetPlaySource returns one followed package, or ErrNotFound.
func (s *Store) GetPlaySource(ctx context.Context, packageName string) (*PlaySource, error) {
	return scanPlaySource(s.pool.QueryRow(ctx,
		`SELECT `+playSourceCols+` FROM play_sources WHERE package_name = $1`, packageName))
}

// PutPlaySource follows a package on Google Play, or changes the tracks and label of one already
// followed. A change of tracks clears the last check's result: what was published on the old
// tracks says nothing about the new ones.
func (s *Store) PutPlaySource(ctx context.Context, packageName string, tracks []string, label string) (*PlaySource, error) {
	return scanPlaySource(s.pool.QueryRow(ctx,
		`INSERT INTO play_sources (package_name, tracks, label) VALUES ($1, $2, $3)
		 ON CONFLICT (package_name) DO UPDATE
		    SET label = EXCLUDED.label,
		        tracks = EXCLUDED.tracks,
		        checked_at = CASE WHEN play_sources.tracks = EXCLUDED.tracks THEN play_sources.checked_at END,
		        published_version = CASE WHEN play_sources.tracks = EXCLUDED.tracks THEN play_sources.published_version END,
		        last_error = CASE WHEN play_sources.tracks = EXCLUDED.tracks THEN play_sources.last_error ELSE '' END
		 RETURNING `+playSourceCols, packageName, tracks, label))
}

// DeletePlaySource stops following a package. The builds already fetched stay in the catalog: they
// are what the phones run, and removing the source is not a decision about them.
func (s *Store) DeletePlaySource(ctx context.Context, packageName string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM play_sources WHERE package_name = $1`, packageName)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// RecordPlayCheck writes the result of one check. A failed check keeps the last published version
// it knew, so the console can still say which build the phones are on while it shows the error.
func (s *Store) RecordPlayCheck(ctx context.Context, packageName string, at time.Time, published *int64, checkErr string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE play_sources
		    SET checked_at = $2,
		        published_version = CASE WHEN $4 = '' THEN $3 ELSE published_version END,
		        last_error = $4
		  WHERE package_name = $1`, packageName, at, published, checkErr)
	return err
}
