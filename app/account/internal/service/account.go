package service

import (
	"context"
	"math"
	"time"

	v1 "github.com/Mars-08042/MarsChat/api/account/v1"
	"github.com/Mars-08042/MarsChat/app/account/internal/biz"
	"github.com/Mars-08042/MarsChat/pkg/clientip"
)

// AccountService 实现对外的 account.v1.AccountService（HTTP 与 gRPC）。
type AccountService struct {
	v1.UnimplementedAccountServiceServer

	codes    *biz.CodeUsecase
	auth     *biz.AuthUsecase
	users    *biz.UserUsecase
	sessions *biz.SessionUsecase
	admin    *biz.AdminUsecase
	ip       *clientip.Resolver
}

// NewAccountService 创建对外服务。
func NewAccountService(codes *biz.CodeUsecase, auth *biz.AuthUsecase, users *biz.UserUsecase,
	sessions *biz.SessionUsecase, admin *biz.AdminUsecase, ip *clientip.Resolver) *AccountService {
	return &AccountService{codes: codes, auth: auth, users: users, sessions: sessions, admin: admin, ip: ip}
}

func (s *AccountService) SendEmailCode(ctx context.Context, req *v1.SendEmailCodeRequest) (*v1.SendEmailCodeResponse, error) {
	cooldown, err := s.codes.Send(ctx, req.GetEmail(), int32(req.GetPurpose()), s.ip.FromContext(ctx))
	if err != nil {
		return nil, err
	}
	return &v1.SendEmailCodeResponse{CooldownSeconds: int32(math.Ceil(cooldown.Seconds()))}, nil
}

func (s *AccountService) Register(ctx context.Context, req *v1.RegisterRequest) (*v1.RegisterResponse, error) {
	u, pair, err := s.auth.Register(ctx, biz.RegisterInput{
		Email:    req.GetEmail(),
		Code:     req.GetCode(),
		Password: req.GetPassword(),
		Nickname: req.GetNickname(),
		Device:   toDevice(req.GetDevice()),
		IP:       s.ip.FromContext(ctx),
	})
	if err != nil {
		return nil, err
	}
	return &v1.RegisterResponse{User: toUser(u), Token: toTokenPair(pair, time.Now())}, nil
}

func (s *AccountService) LoginByPassword(ctx context.Context, req *v1.LoginByPasswordRequest) (*v1.LoginByPasswordResponse, error) {
	u, pair, err := s.auth.LoginByPassword(ctx, req.GetEmail(), req.GetPassword(), toDevice(req.GetDevice()), s.ip.FromContext(ctx))
	if err != nil {
		return nil, err
	}
	return &v1.LoginByPasswordResponse{User: toUser(u), Token: toTokenPair(pair, time.Now())}, nil
}

func (s *AccountService) LoginByEmailCode(ctx context.Context, req *v1.LoginByEmailCodeRequest) (*v1.LoginByEmailCodeResponse, error) {
	u, pair, err := s.auth.LoginByEmailCode(ctx, req.GetEmail(), req.GetCode(), toDevice(req.GetDevice()), s.ip.FromContext(ctx))
	if err != nil {
		return nil, err
	}
	return &v1.LoginByEmailCodeResponse{User: toUser(u), Token: toTokenPair(pair, time.Now())}, nil
}

func (s *AccountService) RefreshToken(ctx context.Context, req *v1.RefreshTokenRequest) (*v1.RefreshTokenResponse, error) {
	pair, err := s.auth.Refresh(ctx, req.GetRefreshToken(), s.ip.FromContext(ctx))
	if err != nil {
		return nil, err
	}
	return &v1.RefreshTokenResponse{Token: toTokenPair(pair, time.Now())}, nil
}

func (s *AccountService) Logout(ctx context.Context, _ *v1.LogoutRequest) (*v1.LogoutResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.auth.Logout(ctx, p); err != nil {
		return nil, err
	}
	return &v1.LogoutResponse{}, nil
}

func (s *AccountService) GetMe(ctx context.Context, _ *v1.GetMeRequest) (*v1.GetMeResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	u, err := s.users.GetMe(ctx, p.UserID)
	if err != nil {
		return nil, err
	}
	return &v1.GetMeResponse{User: toUser(u)}, nil
}

func (s *AccountService) UpdateMe(ctx context.Context, req *v1.UpdateMeRequest) (*v1.UpdateMeResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	upd := biz.ProfileUpdate{
		Nickname:  req.Nickname,
		Avatar:    req.Avatar,
		Signature: req.Signature,
	}
	if req.Gender != nil {
		g := int32(req.GetGender())
		upd.Gender = &g
	}
	if req.Birthday != nil {
		upd.SetBirthday = true
		if upd.Birthday, err = biz.ParseBirthday(req.GetBirthday()); err != nil {
			return nil, err
		}
	}
	u, err := s.users.UpdateMe(ctx, p.UserID, upd)
	if err != nil {
		return nil, err
	}
	return &v1.UpdateMeResponse{User: toUser(u)}, nil
}

func (s *AccountService) GetUser(ctx context.Context, req *v1.GetUserRequest) (*v1.GetUserResponse, error) {
	u, err := s.users.GetUser(ctx, req.GetUserId())
	if err != nil {
		return nil, err
	}
	return &v1.GetUserResponse{User: toProfile(u)}, nil
}

func (s *AccountService) ListSessions(ctx context.Context, _ *v1.ListSessionsRequest) (*v1.ListSessionsResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	list, err := s.sessions.List(ctx, p)
	if err != nil {
		return nil, err
	}
	out := make([]*v1.Session, len(list))
	for i, sess := range list {
		out[i] = toSession(sess, p.SessionID)
	}
	return &v1.ListSessionsResponse{Sessions: out}, nil
}

func (s *AccountService) RevokeSession(ctx context.Context, req *v1.RevokeSessionRequest) (*v1.RevokeSessionResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.sessions.Revoke(ctx, p, req.GetSessionId()); err != nil {
		return nil, err
	}
	return &v1.RevokeSessionResponse{}, nil
}

func (s *AccountService) BanUser(ctx context.Context, req *v1.BanUserRequest) (*v1.BanUserResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.admin.Ban(ctx, p, req.GetUserId()); err != nil {
		return nil, err
	}
	return &v1.BanUserResponse{}, nil
}

func (s *AccountService) UnbanUser(ctx context.Context, req *v1.UnbanUserRequest) (*v1.UnbanUserResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.admin.Unban(ctx, p, req.GetUserId()); err != nil {
		return nil, err
	}
	return &v1.UnbanUserResponse{}, nil
}
