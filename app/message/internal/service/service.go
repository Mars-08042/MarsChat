// Package service 把 proto 接口适配到 biz 用例：参数转换、取当前用户。
// 参数格式由 protovalidate 中间件在进入这里之前校验。
package service

import (
	"context"

	"github.com/google/wire"
	"google.golang.org/protobuf/types/known/timestamppb"

	accountv1 "github.com/Mars-08042/MarsChat/api/account/v1"
	v1 "github.com/Mars-08042/MarsChat/api/message/v1"
	socialv1 "github.com/Mars-08042/MarsChat/api/social/v1"
	"github.com/Mars-08042/MarsChat/app/message/internal/biz"
	"github.com/Mars-08042/MarsChat/pkg/auth"
)

// ProviderSet 是 service 层的依赖提供者。
var ProviderSet = wire.NewSet(NewMessageService, NewInternalService)

// principal 取当前用户。鉴权中间件保证对外接口一定有值，取不到说明路由配置有误。
func principal(ctx context.Context) (auth.Principal, error) {
	p, ok := auth.FromContext(ctx)
	if !ok {
		return auth.Principal{}, auth.ErrUnauthorized("missing principal")
	}
	return p, nil
}

func toMessages(ms []*biz.Message, convType int32) []*v1.Message {
	out := make([]*v1.Message, len(ms))
	for i, m := range ms {
		out[i] = biz.MessageProto(m, convType)
	}
	return out
}

func toConversation(v *biz.ConversationView, me int64) *v1.Conversation {
	c := v.Conversation
	out := &v1.Conversation{
		Id:          c.ID,
		Type:        v1.ConversationType(c.Type),
		MaxSeq:      c.MaxSeq,
		ReadSeq:     v.ReadSeq,
		UnreadCount: v.Unread(),
		PeerReadSeq: v.PeerReadSeq,
	}
	if v.LastMessage != nil {
		out.LastMessage = biz.MessageProto(v.LastMessage, c.Type)
	}
	if c.Type == biz.TypeSingle {
		// account 不可用时只返回对方 ID，客户端显示占位
		out.PeerUser = v.PeerUser
		if out.PeerUser == nil {
			out.PeerUser = &accountv1.UserProfile{Id: c.Peer(me)}
		}
	} else {
		out.Group = &v1.GroupBrief{Id: c.ID}
		if g := v.Group; g != nil {
			out.Group = &v1.GroupBrief{Id: g.ID, Name: g.Name, Avatar: g.Avatar, Status: socialv1.GroupStatus(g.Status), MemberCount: g.MemberCount}
		}
	}
	return out
}

// MessageService 实现 message.v1.MessageService。
type MessageService struct {
	v1.UnimplementedMessageServiceServer

	convs *biz.ConversationUsecase
}

// NewMessageService 创建对外服务。
func NewMessageService(convs *biz.ConversationUsecase) *MessageService {
	return &MessageService{convs: convs}
}

func (s *MessageService) ListConversations(ctx context.Context, req *v1.ListConversationsRequest) (*v1.ListConversationsResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	views, next, err := s.convs.List(ctx, p.UserID, req.GetCursor(), req.GetLimit())
	if err != nil {
		return nil, err
	}
	out := make([]*v1.Conversation, len(views))
	for i, v := range views {
		out[i] = toConversation(v, p.UserID)
	}
	return &v1.ListConversationsResponse{Conversations: out, NextCursor: next}, nil
}

func (s *MessageService) GetConversation(ctx context.Context, req *v1.GetConversationRequest) (*v1.GetConversationResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	v, err := s.convs.Get(ctx, p.UserID, req.GetConversationId())
	if err != nil {
		return nil, err
	}
	return &v1.GetConversationResponse{Conversation: toConversation(v, p.UserID)}, nil
}

func (s *MessageService) ListMessages(ctx context.Context, req *v1.ListMessagesRequest) (*v1.ListMessagesResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	h, err := s.convs.ListMessages(ctx, p.UserID, req.GetConversationId(), req.GetBeforeSeq(), req.GetLimit())
	if err != nil {
		return nil, err
	}
	return &v1.ListMessagesResponse{Messages: toMessages(h.Messages, h.Conversation.Type), NextBeforeSeq: h.NextBeforeSeq}, nil
}

func (s *MessageService) GetDifference(ctx context.Context, req *v1.GetDifferenceRequest) (*v1.GetDifferenceResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	d, err := s.convs.GetDifference(ctx, p.UserID, req.GetConversationId(), req.GetFromSeq(), req.GetLimit())
	if err != nil {
		return nil, err
	}
	return &v1.GetDifferenceResponse{
		Result:      d.Result,
		Messages:    toMessages(d.Messages, d.Conversation.Type),
		NextFromSeq: d.NextFromSeq,
		MaxSeq:      d.MaxSeq,
	}, nil
}

func (s *MessageService) MarkRead(ctx context.Context, req *v1.MarkReadRequest) (*v1.MarkReadResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	seq, err := s.convs.MarkRead(ctx, p.UserID, req.GetConversationId(), req.GetSeq())
	if err != nil {
		return nil, err
	}
	return &v1.MarkReadResponse{ReadSeq: seq}, nil
}

// InternalService 实现 message.v1.MessageInternalService，只注册在 gRPC 上，供 access 调用。
type InternalService struct {
	v1.UnimplementedMessageInternalServiceServer

	msgs *biz.MessageUsecase
}

// NewInternalService 创建内部服务。
func NewInternalService(msgs *biz.MessageUsecase) *InternalService {
	return &InternalService{msgs: msgs}
}

func (s *InternalService) SendMessage(ctx context.Context, req *v1.SendMessageRequest) (*v1.SendMessageResponse, error) {
	om := req.GetMessage()
	m, err := s.msgs.Send(ctx, biz.SendInput{
		SenderID:     req.GetSenderId(),
		ConnectionID: req.GetConnectionId(),
		PeerType:     int32(om.GetPeerType()),
		PeerID:       om.GetPeerId(),
		ClientMsgID:  om.GetClientMsgId(),
		Content:      om.GetContent(),
	})
	if err != nil {
		return nil, err
	}
	return &v1.SendMessageResponse{Result: sendResult(m)}, nil
}

func (s *InternalService) WriteCallRecord(ctx context.Context, req *v1.WriteCallRecordRequest) (*v1.WriteCallRecordResponse, error) {
	m, err := s.msgs.WriteCallRecord(ctx, biz.CallRecordInput{
		CallID:   req.GetCallId(),
		CallerID: req.GetCallerId(),
		CalleeID: req.GetCalleeId(),
		Call:     req.GetCall(),
	})
	if err != nil {
		return nil, err
	}
	return &v1.WriteCallRecordResponse{Result: sendResult(m)}, nil
}

func sendResult(m *biz.Message) *v1.SendResult {
	return &v1.SendResult{ConversationId: m.ConversationID, Seq: m.Seq, CreatedAt: timestamppb.New(m.CreatedAt)}
}
