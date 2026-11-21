//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/proto"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"

	v1 "github.com/Mars-08042/MarsChat/api/account/v1"
	"github.com/Mars-08042/MarsChat/test/testkit"
)

// 依赖本地配置：account 使用 log 发送方和默认固定验证码 000000（app/account/configs/config.yaml）。
// make e2e 会加载 .env，测试用其中的密码直接连接 Redis 和 MySQL 做准备工作。
const (
	accountHTTP = "http://127.0.0.1:18010"
	accountGRPC = "127.0.0.1:19010"
	fixedCode   = testkit.FixedCode
	password    = testkit.Password
)

type apiError = testkit.APIError

// callAt 调用 base 地址上的接口，实现见 testkit.Call。
func callAt(t *testing.T, base, method, path, token string, req, resp proto.Message) (int, *apiError) {
	t.Helper()
	return testkit.Call(t, base, method, path, token, req, resp)
}

// call 调用 account。
func call(t *testing.T, method, path, token string, req, resp proto.Message) (int, *apiError) {
	t.Helper()
	return callAt(t, accountHTTP, method, path, token, req, resp)
}

func mustCall(t *testing.T, method, path, token string, req, resp proto.Message) {
	t.Helper()
	if status, e := call(t, method, path, token, req, resp); e != nil {
		t.Fatalf("%s %s: status %d, %+v", method, path, status, e)
	}
}

func wantError(t *testing.T, status int, e *apiError, wantStatus int, wantReason string) {
	t.Helper()
	if e == nil || status != wantStatus || e.Reason != wantReason {
		t.Fatalf("got status %d, error %+v; want %d %s", status, e, wantStatus, wantReason)
	}
}

// localPart 返回邮箱 @ 之前的部分，用于拼设备 ID（设备 ID 不允许 @ 和 .）。
func localPart(email string) string {
	return testkit.LocalPart(email)
}

func randomEmail(t *testing.T) string {
	t.Helper()
	return testkit.RandomEmail(t)
}

func device(id string, p v1.Platform) *v1.Device {
	return testkit.Device(id, p)
}

func newRedisClient(t *testing.T) *redis.Client {
	t.Helper()
	return testkit.NewRedis(t)
}

// resetRateLimits 清除本机 IP（直连时为 127.0.0.1）的限流计数。
func resetRateLimits(t *testing.T) {
	t.Helper()
	testkit.ResetRateLimits(t, newRedisClient(t), "127.0.0.1")
}

type session struct {
	user  *v1.User
	token *v1.TokenPair
}

func register(t *testing.T, email, deviceID string) session {
	t.Helper()
	mustCall(t, http.MethodPost, "/v1/auth/email-codes", "",
		&v1.SendEmailCodeRequest{Email: email, Purpose: v1.CodePurpose_CODE_PURPOSE_REGISTER}, nil)
	var resp v1.RegisterResponse
	mustCall(t, http.MethodPost, "/v1/auth/register", "", &v1.RegisterRequest{
		Email: email, Code: fixedCode, Password: password, Nickname: "小克",
		Device: device(deviceID, v1.Platform_PLATFORM_WEB),
	}, &resp)
	return session{user: resp.GetUser(), token: resp.GetToken()}
}

func loginByPassword(t *testing.T, email, deviceID string) session {
	t.Helper()
	var resp v1.LoginByPasswordResponse
	mustCall(t, http.MethodPost, "/v1/auth/login/password", "", &v1.LoginByPasswordRequest{
		Email: email, Password: password, Device: device(deviceID, v1.Platform_PLATFORM_ANDROID),
	}, &resp)
	return session{user: resp.GetUser(), token: resp.GetToken()}
}

func TestAccountFlow(t *testing.T) {
	resetRateLimits(t)
	email := randomEmail(t)
	web := register(t, email, "e2e-web-"+localPart(email))
	if web.user.GetEmail() != email || web.user.GetNickname() != "小克" || web.user.GetRole() != v1.Role_ROLE_USER {
		t.Fatalf("registered user = %v", web.user)
	}
	if web.token.GetAccessExpiresIn() != 900 {
		t.Errorf("access_expires_in = %d, want 900", web.token.GetAccessExpiresIn())
	}

	// 发送间隔
	status, e := call(t, http.MethodPost, "/v1/auth/email-codes", "",
		&v1.SendEmailCodeRequest{Email: email, Purpose: v1.CodePurpose_CODE_PURPOSE_LOGIN}, nil)
	wantError(t, status, e, 429, "CODE_SEND_TOO_FREQUENT")
	if e.Metadata["retry_after_seconds"] == "" {
		t.Error("retry_after_seconds missing")
	}

	var me v1.GetMeResponse
	mustCall(t, http.MethodGet, "/v1/users/me", web.token.GetAccessToken(), nil, &me)
	if me.GetUser().GetId() != web.user.GetId() {
		t.Fatalf("GetMe = %v", me.GetUser())
	}

	nick, birthday := "新昵称", "2000-02-29"
	var upd v1.UpdateMeResponse
	mustCall(t, http.MethodPatch, "/v1/users/me", web.token.GetAccessToken(),
		&v1.UpdateMeRequest{Nickname: &nick, Birthday: &birthday}, &upd)
	if upd.GetUser().GetNickname() != nick || upd.GetUser().GetBirthday() != birthday {
		t.Fatalf("UpdateMe = %v", upd.GetUser())
	}
	// 缓存已删除，再读拿到新值
	mustCall(t, http.MethodGet, "/v1/users/me", web.token.GetAccessToken(), nil, &me)
	if me.GetUser().GetNickname() != nick {
		t.Fatalf("GetMe after update = %v", me.GetUser())
	}

	// 第二台设备
	android := loginByPassword(t, email, "e2e-android-"+localPart(email))
	var list v1.ListSessionsResponse
	mustCall(t, http.MethodGet, "/v1/sessions", web.token.GetAccessToken(), nil, &list)
	if len(list.GetSessions()) != 2 {
		t.Fatalf("sessions = %d, want 2", len(list.GetSessions()))
	}
	var androidSID int64
	for _, s := range list.GetSessions() {
		if !s.GetCurrent() {
			androidSID = s.GetId()
		}
	}

	// 刷新令牌轮换；宽限期内重复使用旧令牌返回 409
	var refreshed v1.RefreshTokenResponse
	mustCall(t, http.MethodPost, "/v1/auth/refresh", "", &v1.RefreshTokenRequest{RefreshToken: web.token.GetRefreshToken()}, &refreshed)
	status, e = call(t, http.MethodPost, "/v1/auth/refresh", "", &v1.RefreshTokenRequest{RefreshToken: web.token.GetRefreshToken()}, nil)
	wantError(t, status, e, 409, "REFRESH_TOKEN_ROTATED")
	web.token = refreshed.GetToken()

	// 踢掉第二台设备：它的访问令牌立即失效，刷新令牌也不能再用
	mustCall(t, http.MethodDelete, "/v1/sessions/"+strconv.FormatInt(androidSID, 10), web.token.GetAccessToken(), nil, nil)
	status, e = call(t, http.MethodGet, "/v1/users/me", android.token.GetAccessToken(), nil, nil)
	wantError(t, status, e, 401, "UNAUTHORIZED")
	status, e = call(t, http.MethodPost, "/v1/auth/refresh", "", &v1.RefreshTokenRequest{RefreshToken: android.token.GetRefreshToken()}, nil)
	wantError(t, status, e, 401, "REFRESH_TOKEN_INVALID")

	// 退出
	mustCall(t, http.MethodPost, "/v1/auth/logout", web.token.GetAccessToken(), &v1.LogoutRequest{}, nil)
	status, e = call(t, http.MethodGet, "/v1/users/me", web.token.GetAccessToken(), nil, nil)
	wantError(t, status, e, 401, "UNAUTHORIZED")

	// 验证码登录。注册时发过码，先清掉发送间隔
	newRedisClient(t).Del(context.Background(), "account:email_code:cooldown:"+email)
	mustCall(t, http.MethodPost, "/v1/auth/email-codes", "", &v1.SendEmailCodeRequest{Email: email, Purpose: v1.CodePurpose_CODE_PURPOSE_LOGIN}, nil)
	var codeLogin v1.LoginByEmailCodeResponse
	mustCall(t, http.MethodPost, "/v1/auth/login/email-code", "", &v1.LoginByEmailCodeRequest{
		Email: email, Code: fixedCode, Device: device("e2e-web-"+localPart(email), v1.Platform_PLATFORM_WEB),
	}, &codeLogin)
}

func TestRefreshTokenReuseRevokesSession(t *testing.T) {
	resetRateLimits(t)
	s := register(t, randomEmail(t), "e2e-reuse-device")
	var refreshed v1.RefreshTokenResponse
	mustCall(t, http.MethodPost, "/v1/auth/refresh", "", &v1.RefreshTokenRequest{RefreshToken: s.token.GetRefreshToken()}, &refreshed)

	// 超过 10 秒宽限期后旧令牌再次出现，判定为泄露
	time.Sleep(11 * time.Second)
	status, e := call(t, http.MethodPost, "/v1/auth/refresh", "", &v1.RefreshTokenRequest{RefreshToken: s.token.GetRefreshToken()}, nil)
	wantError(t, status, e, 401, "REFRESH_TOKEN_INVALID")
	status, e = call(t, http.MethodPost, "/v1/auth/refresh", "", &v1.RefreshTokenRequest{RefreshToken: refreshed.GetToken().GetRefreshToken()}, nil)
	wantError(t, status, e, 401, "REFRESH_TOKEN_INVALID")
	status, e = call(t, http.MethodGet, "/v1/users/me", refreshed.GetToken().GetAccessToken(), nil, nil)
	wantError(t, status, e, 401, "UNAUTHORIZED")
}

// grantAdmin 与 make grant-admin 等价：改数据库并删除资料缓存，再刷新令牌拿到新角色。
func grantAdmin(t *testing.T, s session) session {
	t.Helper()
	dsn := fmt.Sprintf("account:%s@tcp(127.0.0.1:13306)/marschat_account?parseTime=true&loc=UTC", os.Getenv("MARSCHAT_ACCOUNT_DB_PASSWORD"))
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open mysql: %v", err)
	}
	if sqlDB, err := db.DB(); err == nil {
		defer sqlDB.Close()
	}
	if err := db.Exec("UPDATE users SET role = 2 WHERE id = ?", s.user.GetId()).Error; err != nil {
		t.Fatal(err)
	}
	newRedisClient(t).Del(context.Background(), "account:user:v2:"+strconv.FormatInt(s.user.GetId(), 10))
	var refreshed v1.RefreshTokenResponse
	mustCall(t, http.MethodPost, "/v1/auth/refresh", "", &v1.RefreshTokenRequest{RefreshToken: s.token.GetRefreshToken()}, &refreshed)
	s.token = refreshed.GetToken()
	return s
}

func TestBanAndUnban(t *testing.T) {
	resetRateLimits(t)
	admin := grantAdmin(t, register(t, randomEmail(t), "e2e-admin-device"))
	email := randomEmail(t)
	target := register(t, email, "e2e-target-device")
	banPath := "/v1/admin/users/" + strconv.FormatInt(target.user.GetId(), 10)

	status, e := call(t, http.MethodPost, "/v1/admin/users/"+strconv.FormatInt(admin.user.GetId(), 10)+"/ban", target.token.GetAccessToken(), &v1.BanUserRequest{}, nil)
	wantError(t, status, e, 403, "PERMISSION_DENIED")

	mustCall(t, http.MethodPost, banPath+"/ban", admin.token.GetAccessToken(), &v1.BanUserRequest{}, nil)
	status, e = call(t, http.MethodGet, "/v1/users/me", target.token.GetAccessToken(), nil, nil)
	wantError(t, status, e, 401, "UNAUTHORIZED")
	status, e = call(t, http.MethodPost, "/v1/auth/refresh", "", &v1.RefreshTokenRequest{RefreshToken: target.token.GetRefreshToken()}, nil)
	wantError(t, status, e, 401, "REFRESH_TOKEN_INVALID")
	status, e = call(t, http.MethodPost, "/v1/auth/login/password", "", &v1.LoginByPasswordRequest{
		Email: email, Password: password, Device: device("e2e-target-device", v1.Platform_PLATFORM_WEB),
	}, nil)
	wantError(t, status, e, 403, "USER_BANNED")
	var profile v1.GetUserResponse
	mustCall(t, http.MethodGet, "/v1/users/"+strconv.FormatInt(target.user.GetId(), 10), admin.token.GetAccessToken(), nil, &profile)
	if profile.GetUser().GetStatus() != v1.UserStatus_USER_STATUS_BANNED {
		t.Fatalf("status after ban = %v", profile.GetUser().GetStatus())
	}

	mustCall(t, http.MethodPost, banPath+"/unban", admin.token.GetAccessToken(), &v1.UnbanUserRequest{}, nil)
	// iat 按秒计，封禁时间所在的那一秒签发的令牌仍会被拒绝，等到下一秒再登录
	time.Sleep(time.Second)
	again := loginByPassword(t, email, "e2e-target-device")
	mustCall(t, http.MethodGet, "/v1/users/me", again.token.GetAccessToken(), nil, nil)
}

func TestPublicProfileAndInternalBatchGet(t *testing.T) {
	resetRateLimits(t)
	a := register(t, randomEmail(t), "e2e-profile-a")
	b := register(t, randomEmail(t), "e2e-profile-b")

	// 公开资料不含邮箱；新注册的用户已加入布隆过滤器，能查到
	status, raw := getRaw(t, "/v1/users/"+strconv.FormatInt(b.user.GetId(), 10), a.token.GetAccessToken())
	if status != 200 || bytes.Contains(raw, []byte(b.user.GetEmail())) {
		t.Fatalf("GetUser status %d body %s", status, raw)
	}
	st, e := call(t, http.MethodGet, "/v1/users/12345", a.token.GetAccessToken(), nil, nil)
	wantError(t, st, e, 404, "USER_NOT_FOUND")

	conn, err := grpc.NewClient(accountGRPC, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := v1.NewAccountInternalServiceClient(conn).BatchGetUsers(ctx, &v1.BatchGetUsersRequest{
		UserIds: []int64{b.user.GetId(), 12345, a.user.GetId()},
	})
	if err != nil {
		t.Fatalf("BatchGetUsers: %v", err)
	}
	if len(resp.GetUsers()) != 2 || resp.GetUsers()[0].GetId() != b.user.GetId() || resp.GetUsers()[1].GetId() != a.user.GetId() {
		t.Fatalf("BatchGetUsers = %v", resp.GetUsers())
	}
}

func TestAuthAndValidationErrors(t *testing.T) {
	resetRateLimits(t)
	status, e := call(t, http.MethodGet, "/v1/users/me", "", nil, nil)
	wantError(t, status, e, 401, "UNAUTHORIZED")
	status, e = call(t, http.MethodGet, "/v1/users/me", "not-a-jwt", nil, nil)
	wantError(t, status, e, 401, "UNAUTHORIZED")
	status, e = call(t, http.MethodPost, "/v1/auth/email-codes", "", &v1.SendEmailCodeRequest{Email: "12345", Purpose: v1.CodePurpose_CODE_PURPOSE_REGISTER}, nil)
	wantError(t, status, e, 400, "VALIDATOR")
	status, e = call(t, http.MethodPost, "/v1/auth/login/password", "", &v1.LoginByPasswordRequest{
		Email: randomEmail(t), Password: "whatever1", Device: device("e2e-nobody", v1.Platform_PLATFORM_WEB),
	}, nil)
	wantError(t, status, e, 401, "INVALID_CREDENTIALS")
}

func getRaw(t *testing.T, path, token string) (int, []byte) {
	t.Helper()
	r, _ := http.NewRequest(http.MethodGet, accountHTTP+path, nil)
	r.Header.Set("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, b
}
