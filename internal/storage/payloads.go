package storage

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shadowsafin/synapass/internal/domain"
)

// maxPayloadBytes caps one captured prompt. Image parts and long histories can
// make a request body megabytes; the capture path keeps the leading messages
// that fit and marks the truncation rather than storing unbounded blobs.
const maxPayloadBytes = 64 * 1024

// PayloadRepository stores captured request prompts for offline replay.
type PayloadRepository struct {
	pool   *pgxpool.Pool
	logger *slog.Logger
}

// NewPayloadRepository constructs the store.
func NewPayloadRepository(pool *pgxpool.Pool, logger *slog.Logger) *PayloadRepository {
	if logger == nil {
		logger = slog.Default()
	}
	return &PayloadRepository{pool: pool, logger: logger}
}

// Save inserts or replaces the captured payload for a request id. It never
// returns a marshal error: an oversized prompt degrades to truncated text,
// because losing the capture must not lose the request it describes.
func (r *PayloadRepository) Save(ctx context.Context, payload *domain.RequestPayload) error {
	if payload == nil || payload.RequestID == "" {
		return nil
	}
	messages := payload.Messages
	encoded, err := json.Marshal(messages)
	if err != nil {
		r.logger.Warn("failed to encode request payload; storing a text fallback",
			"request_id", payload.RequestID, "error", err)
		messages = textFallback(messages)
		encoded, _ = json.Marshal(messages)
	}
	if len(encoded) > maxPayloadBytes {
		messages = textFallback(messages)
		encoded, _ = json.Marshal(messages)
		if len(encoded) > maxPayloadBytes {
			// A single text fallback still over the cap (pathological input):
			// keep the head bytes' worth as plain text rather than nothing.
			messages = messages[:0]
			encoded, _ = json.Marshal(messages)
		}
	}
	_, err = r.pool.Exec(ctx, `
		INSERT INTO request_payloads (request_id, tenant_id, model, messages, max_output_tokens, payload_bytes)
		VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (request_id) DO UPDATE SET
			tenant_id=EXCLUDED.tenant_id, model=EXCLUDED.model,
			messages=EXCLUDED.messages, max_output_tokens=EXCLUDED.max_output_tokens,
			payload_bytes=EXCLUDED.payload_bytes`,
		payload.RequestID, nullableUUID(payload.TenantID), payload.Model,
		encoded, payload.MaxOutputTokens, len(encoded))
	if err != nil {
		return wrapDBError("save request payload", err)
	}
	return nil
}

// Get returns the captured payload for a request id, or nil when the request
// predates prompt capture or was never stored.
func (r *PayloadRepository) Get(ctx context.Context, requestID string) (*domain.RequestPayload, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT request_id, COALESCE(tenant_id::text,''), model, messages,
			max_output_tokens, created_at
		FROM request_payloads WHERE request_id=$1`, requestID)
	var out domain.RequestPayload
	var raw []byte
	if err := row.Scan(&out.RequestID, &out.TenantID, &out.Model, &raw,
		&out.MaxOutputTokens, &out.CreatedAt); err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, wrapDBError("load request payload", err)
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &out.Messages); err != nil {
			return nil, wrapDBError("decode request payload", err)
		}
	}
	return &out, nil
}

// DeleteBefore removes captures older than the cutoff, returning the row
// count. It is called alongside request-log retention so the two stores age
// out together.
func (r *PayloadRepository) DeleteBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	tag, err := r.pool.Exec(ctx, `DELETE FROM request_payloads WHERE created_at < $1`, cutoff)
	if err != nil {
		return 0, wrapDBError("prune request payloads", err)
	}
	return tag.RowsAffected(), nil
}

// textFallback renders messages as truncated plain text in a single user
// message. It preserves what was asked at the cost of role structure, which
// beats preserving nothing when a prompt does not fit the capture budget.
func textFallback(messages []domain.ChatMessage) []domain.ChatMessage {
	var sb []byte
	for _, m := range messages {
		text := m.Content.PlainText()
		if text == "" {
			continue
		}
		sb = append(sb, '[')
		sb = append(sb, string(m.Role)...)
		sb = append(sb, "] "...)
		sb = append(sb, text...)
		sb = append(sb, '\n')
		if len(sb) > maxPayloadBytes/4 {
			sb = append(sb, "[truncated for capture]"...)
			break
		}
	}
	if len(sb) == 0 {
		return nil
	}
	return []domain.ChatMessage{{Role: domain.RoleUser, Content: domain.NewTextContent(string(sb))}}
}
