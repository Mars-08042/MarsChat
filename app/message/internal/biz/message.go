package biz

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	v1 "github.com/Mars-08042/MarsChat/api/message/v1"
)

// MessageUsecase 处理发送。
type MessageUsecase struct {
	tx      Transaction
	convs   ConversationRepo
	members MemberRepo
	msgs    MessageRepo
	events  EventRepo
	perm    PermissionChecker
	files   FileService
	limiter RateLimiter
	ids     IDGenerator
	policy  *PolicyHolder
	logger  *slog.Logger
}

// NewMessageUsecase 创建发送用例。
func NewMessageUsecase(tx Transaction, convs ConversationRepo, members MemberRepo, msgs MessageRepo, events EventRepo,
	perm PermissionChecker, files FileService, limiter RateLimiter, ids IDGenerator, policy *PolicyHolder, logger *slog.Logger) *MessageUsecase {
	return &MessageUsecase{tx: tx, convs: convs, members: members, msgs: msgs, events: events,
		perm: perm, files: files, limiter: limiter, ids: ids, policy: policy, logger: logger}
}

// SendInput 是一次发送请求。发送者和连接来自 access 上已鉴权的连接。
type SendInput struct {
	SenderID     int64
	ConnectionID int64
	PeerType     int32
	PeerID       int64
	ClientMsgID  string
	Content      *v1.MessageContent
}

// Send 写入一条消息并返回它；同一发送者在同一会话中重复的 client_msg_id 返回首次写入的消息。
//
// 顺序（设计 3.4）：限流 → 只读地解析会话 → 幂等快速路径 → 调用 social 校验权限（事务外）
// → 图片、文件消息向 media 核实文件（事务外）→ 事务内锁会话行分配 seq、写消息、推进发送者已读位置、写 Outbox 事件。
func (uc *MessageUsecase) Send(ctx context.Context, in SendInput) (*Message, error) {
	if err := uc.checkRate(ctx, in.SenderID); err != nil {
		return nil, err
	}

	var (
		known     *Conversation
		low, high int64
		err       error
	)
	switch in.PeerType {
	case TypeGroup:
		known, err = uc.convs.Get(ctx, in.PeerID)
	case TypeSingle:
		low, high = min(in.SenderID, in.PeerID), max(in.SenderID, in.PeerID)
		known, err = uc.convs.FindPair(ctx, low, high)
	default:
		return nil, fmt.Errorf("unknown peer type %d", in.PeerType)
	}
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, err
	}

	// 幂等快速路径：重发时直接返回首次的结果，不再校验权限（首次已经被接受）
	if known != nil {
		m, err := uc.msgs.FindByClientID(ctx, known.ID, in.SenderID, in.ClientMsgID)
		if err == nil {
			return m, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return nil, err
		}
	}

	if err := uc.perm.CheckSend(ctx, in.SenderID, in.PeerType, in.PeerID); err != nil {
		if errors.Is(err, ErrUnavailable) {
			uc.logger.Warn("check send permission failed", slog.Int64("sender_id", in.SenderID), slog.Any("error", err))
			return nil, ErrDependencyUnavailable()
		}
		return nil, err
	}
	content, err := uc.resolveContent(ctx, in.SenderID, in.Content)
	if err != nil {
		return nil, err
	}

	var (
		created *Message
		convID  int64
	)
	err = uc.tx.InTx(ctx, func(ctx context.Context) error {
		now := nowUTC()
		c, err := uc.lockConversation(ctx, in, known, low, high, now)
		if err != nil {
			return err
		}
		convID = c.ID
		m := &Message{
			ConversationID: c.ID,
			Seq:            c.MaxSeq + 1,
			SenderID:       in.SenderID,
			ClientMsgID:    in.ClientMsgID,
			Content:        content,
			CreatedAt:      now,
		}
		// 撞上 uk_client 时整个事务回滚，max_seq 不变，seq 不会出现空洞
		if err := uc.msgs.Create(ctx, m); err != nil {
			return err
		}
		if err := uc.convs.SetMaxSeq(ctx, c.ID, m.Seq, now); err != nil {
			return err
		}
		// 回复视为已读到这里；群成员行还没由事件建立时不更新
		if _, err := uc.members.AdvanceRead(ctx, c.ID, in.SenderID, m.Seq, now); err != nil {
			return err
		}
		ev := &v1.MessageCreated{Message: MessageProto(m, c.Type), OriginConnectionId: in.ConnectionID}
		if c.Type == TypeSingle {
			ev.MemberIds = []int64{c.UserLow, c.UserHigh}
		}
		if err := uc.events.Add(ctx, TopicMessageEvents, idKey(c.ID), ev, now); err != nil {
			return err
		}
		created = m
		return nil
	})
	if errors.Is(err, ErrDuplicate) {
		// 同一条消息的另一次发送已经提交（客户端重发与首次请求并发），返回首次的结果
		return uc.msgs.FindByClientID(ctx, convID, in.SenderID, in.ClientMsgID)
	}
	if err != nil {
		return nil, err
	}
	return created, nil
}

// resolveContent 核实图片、文件消息引用的文件，并用 media 的记录重建内容：名称、大小、类型和尺寸都不信任客户端。
// 文件不存在、访问密钥不匹配、不是聊天文件、图片消息引用的不是带缩略图的图片，一律返回 FILE_INVALID，不区分原因。
// 不要求发送者是上传者：转发时访问密钥随消息一起转发，能看到消息的人就能使用文件。
// 文本消息不调用 media，media 故障不影响文本消息。
func (uc *MessageUsecase) resolveContent(ctx context.Context, senderID int64, c *v1.MessageContent) (*v1.MessageContent, error) {
	var (
		fileID    int64
		accessKey string
		image     bool
	)
	switch b := c.GetBody().(type) {
	case *v1.MessageContent_Image:
		fileID, accessKey, image = b.Image.GetFileId(), b.Image.GetAccessKey(), true
	case *v1.MessageContent_File:
		fileID, accessKey = b.File.GetFileId(), b.File.GetAccessKey()
	default:
		return c, nil
	}
	files, err := uc.files.GetFiles(ctx, []int64{fileID})
	if err != nil {
		if errors.Is(err, ErrUnavailable) {
			uc.logger.Warn("get file from media failed", slog.Int64("sender_id", senderID), slog.Int64("file_id", fileID), slog.Any("error", err))
			return nil, ErrDependencyUnavailable()
		}
		return nil, err
	}
	f := files[fileID]
	if f == nil || f.Purpose != FilePurposeChat || subtle.ConstantTimeCompare([]byte(f.AccessKey), []byte(accessKey)) != 1 {
		return nil, ErrFileInvalid()
	}
	if image {
		if f.Kind != FileKindImage || !f.HasThumbnail {
			return nil, ErrFileInvalid()
		}
		return &v1.MessageContent{Body: &v1.MessageContent_Image{Image: &v1.ImageContent{
			FileId: f.ID, AccessKey: f.AccessKey, Name: f.Name, Size: f.Size, MimeType: f.MimeType, Width: f.Width, Height: f.Height,
		}}}, nil
	}
	return &v1.MessageContent{Body: &v1.MessageContent_File{File: &v1.FileContent{
		FileId: f.ID, AccessKey: f.AccessKey, Name: f.Name, Size: f.Size, MimeType: f.MimeType,
	}}}, nil
}

// lockConversation 锁住会话行，会话不存在时先创建。
//
// 先在事务外做过一次只读查询：会话已存在时直接按主键加锁，只加记录锁；
// 不存在时先插入再加锁。对不存在的主键直接 SELECT ... FOR UPDATE 会加间隙锁，
// 两个并发的首条消息各持一把间隙锁再插入，会互相死锁。
func (uc *MessageUsecase) lockConversation(ctx context.Context, in SendInput, known *Conversation, low, high int64, now time.Time) (*Conversation, error) {
	if known != nil {
		return uc.convs.Lock(ctx, known.ID)
	}
	if in.PeerType == TypeGroup {
		// 群会话通常已由 GroupCreated 事件建立，这里只在事件还没处理时兜底；成员仍由事件维护
		if err := uc.convs.CreateIfAbsent(ctx, &Conversation{ID: in.PeerID, Type: TypeGroup, CreatedAt: now, UpdatedAt: now}); err != nil {
			return nil, err
		}
		return uc.convs.Lock(ctx, in.PeerID)
	}
	id, err := nextID(uc.ids)
	if err != nil {
		return nil, err
	}
	if err := uc.convs.CreateIfAbsent(ctx, &Conversation{
		ID: id, Type: TypeSingle, UserLow: low, UserHigh: high, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		return nil, err
	}
	// 可能被并发的首条消息抢先创建，以 uk_pair 查到的为准
	c, err := uc.convs.LockPair(ctx, low, high)
	if err != nil {
		return nil, err
	}
	// 单聊成员固定是两个人，随会话一起建立，已存在时忽略
	err = uc.members.AddIgnore(ctx, []*Member{
		{ConversationID: c.ID, UserID: low, JoinedAt: now, UpdatedAt: now},
		{ConversationID: c.ID, UserID: high, JoinedAt: now, UpdatedAt: now},
	})
	if err != nil {
		return nil, err
	}
	return c, nil
}

// checkRate 按用户限制发消息频率，所有设备、所有会话合计。Redis 故障时放行并记 WARN：
// 限流用于防刷，不值得为它让发消息整体不可用。
func (uc *MessageUsecase) checkRate(ctx context.Context, userID int64) error {
	l := uc.policy.Load().GetSendMessage()
	key := "message:rl:send:" + strconv.FormatInt(userID, 10)
	res, err := uc.limiter.Allow(ctx, key, int(l.GetLimit()), l.GetWindow().AsDuration())
	if err != nil {
		uc.logger.Warn("rate limiter unavailable", slog.String("limit", "send_message"), slog.Any("error", err))
		return nil
	}
	if !res.Allowed {
		return ErrRateLimited(res.RetryAfter)
	}
	return nil
}

// MessageProto 把消息转换成 proto，供事件和接口使用。
func MessageProto(m *Message, convType int32) *v1.Message {
	return &v1.Message{
		ConversationId:   m.ConversationID,
		ConversationType: v1.ConversationType(convType),
		Seq:              m.Seq,
		SenderId:         m.SenderID,
		ClientMsgId:      m.ClientMsgID,
		Content:          m.Content,
		CreatedAt:        timestamppb.New(m.CreatedAt),
	}
}
