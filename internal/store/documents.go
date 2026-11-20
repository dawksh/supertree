package store

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/dawksh/supertree/internal/command"
)

type Document struct {
	ID       string
	Revision int64
	Data     json.RawMessage
}

func (s *Store) Documents(ctx context.Context, kind string) ([]Document, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id,revision,data FROM documents WHERE kind=? ORDER BY id", kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Document{}
	for rows.Next() {
		var doc Document
		var data string
		if err := rows.Scan(&doc.ID, &doc.Revision, &data); err != nil {
			return nil, err
		}
		doc.Data = json.RawMessage(data)
		result = append(result, doc)
	}
	return result, rows.Err()
}

// PutDocument creates with expected=0, otherwise updates only that revision.
// Callers use the returned revision to reject stale human/agent actions.
func (s *Store) PutDocument(ctx context.Context, kind, id string, value any, expected int64) (int64, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return 0, err
	}
	if expected == 0 {
		result, err := s.db.ExecContext(ctx, "INSERT INTO documents(kind,id,revision,data) VALUES(?,?,1,?) ON CONFLICT(kind,id) DO NOTHING", kind, id, string(data))
		if err != nil {
			return 0, err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return 0, err
		}
		if n == 1 {
			return 1, nil
		}
	} else {
		result, err := s.db.ExecContext(ctx, "UPDATE documents SET revision=revision+1,data=? WHERE kind=? AND id=? AND revision=?", string(data), kind, id, expected)
		if err != nil {
			return 0, err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return 0, err
		}
		if n == 1 {
			return expected + 1, nil
		}
	}
	return 0, &command.Error{Code: "conflict", Message: fmt.Sprintf("%s %s changed; inspect it before retrying", kind, id)}
}
