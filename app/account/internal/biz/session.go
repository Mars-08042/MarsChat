package biz

import (
	"context"
	"log/slog"
	"time"

	"github.com/Mars-08042/MarsChat/pkg/auth"
)

// SessionUsecase 负责设备列表和踢设备。
type SessionUsecase struct {
	sessions SessionRepo
	events   EventRepo
	tx       Transaction
	tokens   *TokenService
	revoker  Revoker
	logger   *slog.Logger
	now      func() time.Time
}

// NewSessionUsecase 创建会话用例。
func NewSessionUsecase(sessions SessionRepo, events EventRepo, tx Transaction, tokens *TokenService, revoker Revoker, logger *slog.Logger) *SessionUsecase {
	return &SessionUsecase{
		sessions: sessions, events: events, tx: tx, tokens: tokens, revoker: revoker, logger: logger,
		now: nowUTC,
	}
}

// List 返回当前用户全部有效会话。
func (uc *SessionUsecase) List(ctx context.Context, p auth.Principal) ([]*Session, error) {
	return uc.sessions.ListActive(ctx, p.UserID)
}

// Revoke 踢掉当前用户的某个设备，也可以踢掉自己当前的会话。
func (uc *SessionUsecase) Revoke(ctx context.Context, p auth.Principal, sid int64) error {
	now := uc.now()
	revoked, err := revokeSessionTx(ctx, uc.tx, uc.sessions, uc.events, p.UserID, sid, ReasonKicked, now)
	if err != nil {
		return err
	}
	if !revoked {
		// 不存在、属于别人和已下线不做区分，避免探测他人的会话 ID
		return ErrSessionNotFound()
	}
	revokeAfterCommit(ctx, uc.revoker, uc.tokens.RevocationTTL(), uc.logger, sid)
	return nil
}
