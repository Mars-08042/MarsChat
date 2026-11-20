//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"

	accessv1 "github.com/Mars-08042/MarsChat/api/access/v1"
	accountv1 "github.com/Mars-08042/MarsChat/api/account/v1"
	messagev1 "github.com/Mars-08042/MarsChat/api/message/v1"
	socialv1 "github.com/Mars-08042/MarsChat/api/social/v1"
	"github.com/Mars-08042/MarsChat/pkg/kafka"
	"github.com/Mars-08042/MarsChat/test/testkit"
)

// 推送链路：message → Outbox → Kafka → delivery → access。除 access 依赖的四个服务外，delivery 也要启动。

// 多端同步：发送者的其他连接收到推送，发起发送的连接只收到 SendAck；已读位置推给本人和对方。
func TestDeliverySingleChatMultiDevice(t *testing.T) {
	resetRateLimits(t)
	a, b := newUser(t), newUser(t)
	makeFriends(t, a, b)
	a1, a2, b1 := dialWS(t), dialWS(t), dialWS(t)
	a1.Auth(a.token)
	a2.Auth(a.token)
	b1.Auth(b.token)

	single := messagev1.ConversationType_CONVERSATION_TYPE_SINGLE
	ack := a1.SendText(1, single, b.id, "d1").GetSendAck()
	if ack.GetSeq() != 1 {
		t.Fatalf("send ack = %v", ack)
	}
	conv := ack.GetConversationId()
	got := b1.WaitFrame("message push to peer", testkit.IsMessage(conv, 1)).GetMessage()
	if got.GetSenderId() != a.id || got.GetContent().GetText().GetText() != "ws d1" || got.GetClientMsgId() != "d1" {
		t.Fatalf("pushed message = %v", got)
	}
	a2.WaitFrame("message push to sender's other connection", testkit.IsMessage(conv, 1))
	a1.ExpectNoMessage(2)

	mustMsg(t, http.MethodPost, fmt.Sprintf("/v1/conversations/%d/read", conv), b.token, &messagev1.MarkReadRequest{Seq: 1}, nil)
	b1.WaitFrame("read push to self", testkit.IsRead(conv, b.id, 1))
	a1.WaitFrame("read receipt", testkit.IsRead(conv, b.id, 1))
	a2.WaitFrame("read receipt on other connection", testkit.IsRead(conv, b.id, 1))
}

// 群消息推给全部在线成员；被移出群后不再收到。
func TestDeliveryGroupPush(t *testing.T) {
	resetRateLimits(t)
	owner, m1, m2 := newUser(t), newUser(t), newUser(t)
	makeFriends(t, owner, m1)
	makeFriends(t, owner, m2)
	var created socialv1.CreateGroupResponse
	mustSocial(t, http.MethodPost, "/v1/groups", owner.token, &socialv1.CreateGroupRequest{
		Name: "推送测试群", JoinMode: socialv1.JoinMode_JOIN_MODE_FREE, MemberIds: []int64{m1.id, m2.id},
	}, &created)
	group := created.GetGroup().GetId()
	o, c1, c2 := dialWS(t), dialWS(t), dialWS(t)
	o.Auth(owner.token)
	c1.Auth(m1.token)
	c2.Auth(m2.token)

	gt := messagev1.ConversationType_CONVERSATION_TYPE_GROUP
	if r := o.SendText(1, gt, group, "g1"); r.GetSendAck().GetSeq() != 1 {
		t.Fatalf("group send = %v", r)
	}
	c1.WaitFrame("group push to m1", testkit.IsMessage(group, 1))
	c2.WaitFrame("group push to m2", testkit.IsMessage(group, 1))

	mustSocial(t, http.MethodDelete, "/v1/groups/"+id(group)+"/members/"+id(m2.id), owner.token, nil, nil)
	if r := o.SendText(2, gt, group, "g2"); r.GetSendAck().GetSeq() != 2 {
		t.Fatalf("group send after removal = %v", r)
	}
	c1.WaitFrame("group push to m1 after removal", testkit.IsMessage(group, 2))
	c2.ExpectNoMessage(3)
}

// 封禁经 UserBanned 事件立即踢掉连接，不必等下一次心跳（心跳路径的原因是 UNSPECIFIED）。
func TestDeliveryKickOnBan(t *testing.T) {
	resetRateLimits(t)
	admin := grantAdmin(t, register(t, randomPhone(t), "e2e-admin-ws"))
	target := register(t, randomPhone(t), "e2e-target-ws")
	c := dialWS(t)
	c.Auth(target.token.GetAccessToken())

	mustCall(t, http.MethodPost, "/v1/admin/users/"+id(target.user.GetId())+"/ban", admin.token.GetAccessToken(), &accountv1.BanUserRequest{}, nil)
	f := c.WaitFrame("kicked frame", func(f *accessv1.ServerFrame) bool { return f.GetKicked() != nil })
	if f.GetKicked().GetReason() != accountv1.RevokeReason_REVOKE_REASON_BANNED {
		t.Fatalf("kicked reason = %v, want BANNED", f.GetKicked().GetReason())
	}
	c.ExpectClose(4002)
}

// social 消费自己的群事件，兜底删除群资料和成员列表缓存（TD-25）。这里直接投递一条 GroupUpdated，
// 模拟同步删除失败后缓存里残留的旧值；message 不处理这类事件，不会受影响。
func TestSocialCacheInvalidatedByEvent(t *testing.T) {
	groupID := time.Now().UnixNano()
	rdb := newRedisClient(t)
	ctx := context.Background()
	keys := []string{"social:group:" + strconv.FormatInt(groupID, 10), "social:group:members:" + strconv.FormatInt(groupID, 10)}
	for _, k := range keys {
		if err := rdb.Set(ctx, k, "stale", time.Hour).Err(); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { rdb.Del(ctx, keys...) })

	payload, err := proto.Marshal(&socialv1.GroupUpdated{Group: &socialv1.Group{Id: groupID}})
	if err != nil {
		t.Fatal(err)
	}
	cl, err := kgo.NewClient(kgo.SeedBrokers(kafka.Brokers(kafkaConf())...))
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	rec := &kgo.Record{
		Topic: kafka.TopicSocialEvents, Key: []byte(strconv.FormatInt(groupID, 10)), Value: payload,
		Headers: []kgo.RecordHeader{
			{Key: kafka.HeaderEventType, Value: []byte("social.v1.GroupUpdated")},
			{Key: kafka.HeaderEventID, Value: []byte("e2e:" + randomHex(t))},
			{Key: kafka.HeaderSource, Value: []byte("e2e")},
		},
	}
	if err := cl.ProduceSync(pctx, rec).FirstErr(); err != nil {
		t.Fatalf("produce: %v", err)
	}

	deadline := time.Now().Add(15 * time.Second)
	for {
		n, err := rdb.Exists(ctx, keys...).Result()
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d of the cache keys still present", n)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
