//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"

	kerrors "github.com/go-kratos/kratos/v3/errors"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/proto"

	messagev1 "github.com/Mars-08042/MarsChat/api/message/v1"
	socialv1 "github.com/Mars-08042/MarsChat/api/social/v1"
)

// message 经 Consul 调用 social（发言权限、群资料）和 account（资料），所以三个服务都要启动。
const (
	messageHTTP = "http://127.0.0.1:18030"
	messageGRPC = "127.0.0.1:19030"
)

func msgCall(t *testing.T, method, path, token string, req, resp proto.Message) (int, *apiError) {
	t.Helper()
	return callAt(t, messageHTTP, method, path, token, req, resp)
}

func mustMsg(t *testing.T, method, path, token string, req, resp proto.Message) {
	t.Helper()
	if status, e := msgCall(t, method, path, token, req, resp); e != nil {
		t.Fatalf("%s %s: status %d, %+v", method, path, status, e)
	}
}

func messageInternal(t *testing.T) messagev1.MessageInternalServiceClient {
	t.Helper()
	conn, err := grpc.NewClient(messageGRPC, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return messagev1.NewMessageInternalServiceClient(conn)
}

func textContent(s string) *messagev1.MessageContent {
	return &messagev1.MessageContent{Body: &messagev1.MessageContent_Text{Text: &messagev1.TextContent{Text: s}}}
}

// sendInternal 直接调用 message 的内部发送接口（access 做的事），连接 ID 固定为 1。
func sendInternal(c messagev1.MessageInternalServiceClient, sender int64, peerType messagev1.ConversationType, peer int64, clientID, text string) (*messagev1.SendResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := c.SendMessage(ctx, &messagev1.SendMessageRequest{
		SenderId: sender, SessionId: 1, ConnectionId: 1,
		Message: &messagev1.OutgoingMessage{PeerType: peerType, PeerId: peer, ClientMsgId: clientID, Content: textContent(text)},
	})
	return resp.GetResult(), err
}

func mustSend(t *testing.T, c messagev1.MessageInternalServiceClient, sender int64, peerType messagev1.ConversationType, peer int64, clientID string) *messagev1.SendResult {
	t.Helper()
	r, err := sendInternal(c, sender, peerType, peer, clientID, "hello "+clientID)
	if err != nil {
		t.Fatalf("send %s: %v", clientID, err)
	}
	return r
}

func getConversation(t *testing.T, token string, id int64) (*messagev1.Conversation, *apiError) {
	t.Helper()
	var resp messagev1.GetConversationResponse
	_, e := msgCall(t, http.MethodGet, "/v1/conversations/"+strconv.FormatInt(id, 10), token, nil, &resp)
	return resp.GetConversation(), e
}

// waitConversation 等待会话出现在用户的视图中。群成员由 social 事件异步维护，有亚秒级延迟。
func waitConversation(t *testing.T, token string, id int64) *messagev1.Conversation {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		c, e := getConversation(t, token, id)
		if e == nil {
			return c
		}
		if e.Reason != "CONVERSATION_NOT_FOUND" || time.Now().After(deadline) {
			t.Fatalf("conversation %d not visible: %+v", id, e)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestMessageSingleChat(t *testing.T) {
	resetRateLimits(t)
	a, b, stranger := newUser(t), newUser(t), newUser(t)
	makeFriends(t, a, b)
	c := messageInternal(t)

	r1 := mustSend(t, c, a.id, messagev1.ConversationType_CONVERSATION_TYPE_SINGLE, b.id, "m1")
	mustSend(t, c, b.id, messagev1.ConversationType_CONVERSATION_TYPE_SINGLE, a.id, "m2")
	r3 := mustSend(t, c, a.id, messagev1.ConversationType_CONVERSATION_TYPE_SINGLE, b.id, "m3")
	if r1.GetSeq() != 1 || r3.GetSeq() != 3 {
		t.Fatalf("seqs = %d, %d", r1.GetSeq(), r3.GetSeq())
	}
	conv := r1.GetConversationId()

	// 同一 client_msg_id 重发：返回首次的 seq，不新增消息
	dup := mustSend(t, c, a.id, messagev1.ConversationType_CONVERSATION_TYPE_SINGLE, b.id, "m1")
	if dup.GetSeq() != 1 || dup.GetConversationId() != conv {
		t.Fatalf("duplicate got %v, want seq 1", dup)
	}

	// b 的会话：a 发了 2 条，b 回复的那条把 b 的已读推进到 2，所以未读 1
	got, e := getConversation(t, b.token, conv)
	if e != nil {
		t.Fatal(e)
	}
	if got.GetMaxSeq() != 3 || got.GetUnreadCount() != 1 || got.GetLastMessage().GetSeq() != 3 ||
		got.GetPeerUser().GetId() != a.id || got.GetPeerUser().GetNickname() == "" {
		t.Fatalf("b's conversation = %v", got)
	}
	var list messagev1.ListConversationsResponse
	mustMsg(t, http.MethodGet, "/v1/conversations", b.token, nil, &list)
	if len(list.GetConversations()) != 1 || list.GetConversations()[0].GetId() != conv {
		t.Fatalf("b's list = %v", list.GetConversations())
	}

	var hist messagev1.ListMessagesResponse
	mustMsg(t, http.MethodGet, fmt.Sprintf("/v1/conversations/%d/messages?limit=2", conv), b.token, nil, &hist)
	if len(hist.GetMessages()) != 2 || hist.GetMessages()[0].GetSeq() != 3 || hist.GetNextBeforeSeq() != 2 ||
		hist.GetMessages()[0].GetContent().GetText().GetText() != "hello m3" {
		t.Fatalf("history = %v", &hist)
	}

	var diff messagev1.GetDifferenceResponse
	mustMsg(t, http.MethodGet, fmt.Sprintf("/v1/conversations/%d/difference?from_seq=1&limit=1", conv), b.token, nil, &diff)
	if diff.GetResult() != messagev1.DifferenceResult_DIFFERENCE_RESULT_SLICE || len(diff.GetMessages()) != 1 || diff.GetNextFromSeq() != 2 {
		t.Fatalf("difference = %v", &diff)
	}

	var read messagev1.MarkReadResponse
	mustMsg(t, http.MethodPost, fmt.Sprintf("/v1/conversations/%d/read", conv), b.token, &messagev1.MarkReadRequest{Seq: 3}, &read)
	if read.GetReadSeq() != 3 {
		t.Fatalf("read_seq = %d", read.GetReadSeq())
	}
	if got, _ = getConversation(t, b.token, conv); got.GetUnreadCount() != 0 {
		t.Errorf("unread after read = %d", got.GetUnreadCount())
	}
	// a 看到 b 的已读位置（已读回执）
	if got, _ = getConversation(t, a.token, conv); got.GetPeerReadSeq() != 3 {
		t.Errorf("peer_read_seq = %d, want 3", got.GetPeerReadSeq())
	}

	// 非好友不能发，原样返回 social 的原因；非成员看不到会话
	_, err := sendInternal(c, stranger.id, messagev1.ConversationType_CONVERSATION_TYPE_SINGLE, a.id, "x", "x")
	if kerrors.Reason(err) != "NOT_FRIENDS" {
		t.Errorf("stranger send: %v", err)
	}
	if _, e := getConversation(t, stranger.token, conv); e == nil || e.Reason != "CONVERSATION_NOT_FOUND" || e.Code != http.StatusNotFound {
		t.Errorf("stranger get: %+v", e)
	}
}

// TestMessageConcurrentSend 两人并发向同一会话发送（夹杂重发），seq 必须从 1 连续递增、没有重复和空洞。
func TestMessageConcurrentSend(t *testing.T) {
	resetRateLimits(t)
	a, b := newUser(t), newUser(t)
	makeFriends(t, a, b)
	c := messageInternal(t)

	// 每人 12 条、每条发两次共 24 次请求，低于每人每 10 秒 30 次的发送限流（重发也计数）
	const perSender = 12
	var (
		mu   sync.Mutex
		seqs = map[string]int64{}
		wg   sync.WaitGroup
		errs []error
	)
	for _, pair := range [][2]user{{a, b}, {b, a}} {
		for i := range perSender {
			wg.Add(1)
			go func(from, to user, i int) {
				defer wg.Done()
				// 每条发两次，模拟客户端超时重发与首次请求并发
				id := fmt.Sprintf("%d-%d", from.id, i)
				for range 2 {
					r, err := sendInternal(c, from.id, messagev1.ConversationType_CONVERSATION_TYPE_SINGLE, to.id, id, id)
					mu.Lock()
					if err != nil {
						errs = append(errs, err)
					} else if prev, ok := seqs[id]; ok && prev != r.GetSeq() {
						errs = append(errs, fmt.Errorf("%s got seq %d and %d", id, prev, r.GetSeq()))
					} else {
						seqs[id] = r.GetSeq()
					}
					mu.Unlock()
				}
			}(pair[0], pair[1], i)
		}
	}
	wg.Wait()
	if len(errs) > 0 {
		t.Fatalf("%d errors, first: %v", len(errs), errs[0])
	}
	all := make([]int64, 0, len(seqs))
	for _, s := range seqs {
		all = append(all, s)
	}
	sort.Slice(all, func(i, j int) bool { return all[i] < all[j] })
	for i, s := range all {
		if s != int64(i+1) {
			t.Fatalf("seqs not contiguous: %v", all)
		}
	}
	if len(all) != 2*perSender {
		t.Fatalf("got %d distinct messages, want %d", len(all), 2*perSender)
	}
}

func TestMessageGroupMembership(t *testing.T) {
	resetRateLimits(t)
	owner, member, joiner := newUser(t), newUser(t), newUser(t)
	makeFriends(t, owner, member)
	var created socialv1.CreateGroupResponse
	mustSocial(t, http.MethodPost, "/v1/groups", owner.token, &socialv1.CreateGroupRequest{
		Name: "消息测试群", JoinMode: socialv1.JoinMode_JOIN_MODE_FREE, MemberIds: []int64{member.id},
	}, &created)
	group := created.GetGroup().GetId()
	c := messageInternal(t)

	// 群会话 ID 就是群 ID；GroupCreated 事件处理后初始成员能看到会话
	waitConversation(t, member.token, group)
	r := mustSend(t, c, owner.id, messagev1.ConversationType_CONVERSATION_TYPE_GROUP, group, "g1")
	if r.GetConversationId() != group || r.GetSeq() != 1 {
		t.Fatalf("group send = %v", r)
	}
	got, _ := getConversation(t, member.token, group)
	if got.GetUnreadCount() != 1 || got.GetGroup().GetName() != "消息测试群" || got.GetType() != messagev1.ConversationType_CONVERSATION_TYPE_GROUP {
		t.Fatalf("member's group conversation = %v", got)
	}

	// 新成员能看历史，但历史不算未读
	mustSocial(t, http.MethodPost, "/v1/groups/"+id(group)+"/join", joiner.token, &socialv1.JoinGroupRequest{}, nil)
	got = waitConversation(t, joiner.token, group)
	if got.GetUnreadCount() != 0 || got.GetReadSeq() != 1 {
		t.Errorf("joiner's conversation = %v", got)
	}
	var hist messagev1.ListMessagesResponse
	mustMsg(t, http.MethodGet, fmt.Sprintf("/v1/conversations/%d/messages", group), joiner.token, nil, &hist)
	if len(hist.GetMessages()) != 1 {
		t.Errorf("joiner history = %d messages, want 1", len(hist.GetMessages()))
	}

	// 被移除后发不了（social 实时校验），会话随事件消失
	mustSocial(t, http.MethodDelete, "/v1/groups/"+id(group)+"/members/"+id(joiner.id), owner.token, nil, nil)
	_, err := sendInternal(c, joiner.id, messagev1.ConversationType_CONVERSATION_TYPE_GROUP, group, "x", "x")
	if kerrors.Reason(err) != "NOT_GROUP_MEMBER" {
		t.Errorf("removed member send: %v", err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		_, e := getConversation(t, joiner.token, group)
		if e != nil && e.Reason == "CONVERSATION_NOT_FOUND" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("removed member still sees the conversation")
		}
		time.Sleep(100 * time.Millisecond)
	}
}
