package store

import (
	"context"
	"encoding/json"
	"fmt"
	"math"

	"github.com/dawksh/supertree/internal/command"
)

type DocumentUpdate struct {
	Kind             string
	ID               string
	Value            any
	ExpectedRevision int64
}

// PutDocuments commits a compound metadata edit or none of it. In particular,
// moving a terminal across buffers cannot persist only its source placeholder.
func (s *Store) PutDocuments(ctx context.Context, updates []DocumentUpdate) ([]Document, error) {
	if len(updates) == 0 || len(updates) > 64 {
		return nil, &command.Error{Code: "invalid_arguments", Message: "document batch must contain 1 through 64 edits"}
	}
	documents := make([]Document, len(updates))
	seen := map[[2]string]bool{}
	for i, update := range updates {
		key := [2]string{update.Kind, update.ID}
		if update.Kind == "" || update.ID == "" || seen[key] || update.ExpectedRevision < 0 || update.ExpectedRevision == math.MaxInt64 {
			return nil, &command.Error{Code: "invalid_arguments", Message: "invalid or duplicate document edit"}
		}
		seen[key] = true
		data, err := json.Marshal(update.Value)
		if err != nil {
			return nil, err
		}
		documents[i] = Document{ID: update.ID, Revision: update.ExpectedRevision + 1, Data: data}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	for i, update := range updates {
		var changed int64
		if update.ExpectedRevision == 0 {
			result, err := tx.ExecContext(ctx, "INSERT INTO documents(kind,id,revision,data) VALUES(?,?,1,?) ON CONFLICT(kind,id) DO NOTHING", update.Kind, update.ID, string(documents[i].Data))
			if err != nil {
				return nil, err
			}
			changed, err = result.RowsAffected()
			if err != nil {
				return nil, err
			}
		} else {
			result, err := tx.ExecContext(ctx, "UPDATE documents SET revision=revision+1,data=? WHERE kind=? AND id=? AND revision=?", string(documents[i].Data), update.Kind, update.ID, update.ExpectedRevision)
			if err != nil {
				return nil, err
			}
			changed, err = result.RowsAffected()
			if err != nil {
				return nil, err
			}
		}
		if changed != 1 {
			return nil, &command.Error{Code: "conflict", Message: fmt.Sprintf("%s %s changed; inspect it before retrying", update.Kind, update.ID)}
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return documents, nil
}
