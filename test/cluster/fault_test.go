//go:build cluster

package cluster

import (
	"context"
	"fmt"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/Mars-08042/MarsChat/pkg/kafka"
	"github.com/Mars-08042/MarsChat/test/testkit"
)

// 故障实验（gateway-multi-instance 设计 3.9 节）。每个实验在持续收发消息时注入一种故障，
// 检查不丢不重，记录恢复情况；结束时（包括失败）恢复容器，不影响后面的测试。go test -short 跳过。

const (
	// 发送间隔：message 限制每人每 10 秒 30 条，间隔 400 毫秒约每 10 秒 25 条
	sendInterval = 400 * time.Millisecond
	redisName    = "marschat-redis-1"
	kafkaName    = "marschat-kafka-1"
)

func skipShort(t *testing.T) {
	if testing.Short() {
		t.Skip("fault experiment skipped in -short mode")
	}
}

// dockerE 对 MarsChat 自己的容器执行 docker 命令（容器名放在最后），容器名必须以 marschat- 开头，
// 不会碰到本机其他项目的容器。
func dockerE(container string, args ...string) (string, error) {
	if !strings.HasPrefix(container, "marschat-") {
		return "", fmt.Errorf("refusing to touch %q: not a MarsChat container", container)
	}
	out, err := exec.Command("docker", append(args, container)...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func docker(t *testing.T, container string, args ...string) string {
	t.Helper()
	out, err := dockerE(container, args...)
	if err != nil {
		t.Fatalf("docker %s %s: %v: %s", strings.Join(args, " "), container, err, out)
	}
	return out
}

// dockerExec 在 MarsChat 的容器内执行命令。
func dockerExec(t *testing.T, container string, cmd ...string) string {
	t.Helper()
	if !strings.HasPrefix(container, "marschat-") {
		t.Fatalf("refusing to touch %q: not a MarsChat container", container)
	}
	out, err := exec.Command("docker", append([]string{"exec", container}, cmd...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker exec %s %v: %v: %s", container, cmd, err, out)
	}
	return string(out)
}

// containerWhere 返回服务的第一个满足条件的容器（marschat-<服务>-1、-2）。
func containerWhere(t *testing.T, service, what string, match func(name string) bool) string {
	t.Helper()
	for i := 1; i <= 2; i++ {
		name := fmt.Sprintf("marschat-%s-%d", service, i)
		if match(name) {
			return name
		}
	}
	t.Fatalf("no %s container %s", service, what)
	return ""
}

// leaseHolder 把 Outbox 租约的持有者（marschat-message-<主机名>-<pid>，容器内主机名是容器 ID）换成容器名。
func leaseHolder(t *testing.T, owner string) string {
	t.Helper()
	rest := strings.TrimPrefix(owner, "marschat-message-")
	host := rest[:max(strings.LastIndex(rest, "-"), 0)]
	if owner == "" || host == "" {
		t.Fatalf("unexpected lease owner %q", owner)
	}
	return containerWhere(t, "message", "with hostname "+host, func(name string) bool {
		return docker(t, name, "inspect", "-f", "{{.Config.Hostname}}") == host
	})
}

// containerByIP 按容器 IP 找服务的容器。
func containerByIP(t *testing.T, service, ip string) string {
	t.Helper()
	return containerWhere(t, service, "with IP "+ip, func(name string) bool {
		return docker(t, name, "inspect", "-f", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}") == ip
	})
}

func hostOf(addr string) string {
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		return addr[:i]
	}
	return addr
}

func waitPassingE(service string, n int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		addrs, err := passingAddrs("marschat-" + service)
		if err == nil && len(addrs) >= n {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("marschat-%s: %d passing instances after %v, want %d (err %v)", service, len(addrs), timeout, n, err)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func waitHealthy(t *testing.T, container string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		out, _ := dockerE(container, "inspect", "-f", "{{.State.Health.Status}}")
		if out == "healthy" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s not healthy after %v (status %q)", container, timeout, out)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// restoreService 在测试结束时启动被杀的容器并等它在 Consul 中恢复，对正在运行的容器是空操作。
func restoreService(t *testing.T, container, service string) {
	t.Cleanup(func() {
		if out, err := dockerE(container, "start"); err != nil {
			t.Errorf("restore %s: %v: %s", container, err, out)
			return
		}
		if err := waitPassingE(service, 2, 60*time.Second); err != nil {
			t.Errorf("restore %s: %v", container, err)
		}
	})
}

// series 是一组按顺序发送的消息，每条失败时用同一个 client_msg_id 重发直到确认。
type series struct {
	prefix   string
	sent     []sent
	failures []sendFailure
}

func newSeries(name string) *series {
	return &series{prefix: name + "-" + strconv.FormatInt(time.Now().UnixNano(), 36)}
}

// send 发送下一条消息，确认的 seq 必须正好是它的序号：重发没有产生第二条，也没有丢号。
func (sr *series) send(t *testing.T, s *session, peer int64, limit time.Duration) sent {
	t.Helper()
	i := len(sr.sent) + 1
	r, err := s.sendUntilAcked(peer, fmt.Sprintf("%s-%d", sr.prefix, i), limit, &sr.failures)
	if err != nil {
		t.Fatal(err)
	}
	if r.seq != int64(i) {
		t.Fatalf("message %d acknowledged with seq %d", i, r.seq)
	}
	sr.sent = append(sr.sent, r)
	return r
}

func (sr *series) n() int64 { return int64(len(sr.sent)) }

// failureSummary 把失败原因按出现次数汇总，用于日志。
func (sr *series) failureSummary() string {
	if len(sr.failures) == 0 {
		return "none"
	}
	counts := map[string]int{}
	for _, f := range sr.failures {
		counts[f.reason]++
	}
	var parts []string
	for r, c := range counts {
		parts = append(parts, fmt.Sprintf("%dx %s", c, r))
	}
	slices.Sort(parts)
	return strings.Join(parts, "; ")
}

// afterRecovery 故障恢复后再发 k 条，接收方必须经推送收到：推送已经恢复，而不是只能靠补拉。
func afterRecovery(t *testing.T, sr *series, from, to *session, peer int64, k int) {
	t.Helper()
	for range k {
		r := sr.send(t, from, peer, 30*time.Second)
		to.waitPushed(t, r.seq, 30*time.Second)
		time.Sleep(sendInterval)
	}
}

// maxPushDelay 返回 sent 中各条从确认到对方收到推送的最大延迟，以及没有收到推送的条数。
func maxPushDelay(s *session, msgs []sent) (time.Duration, int) {
	var worst time.Duration
	missing := 0
	for _, m := range msgs {
		at, ok := s.pushedBy(m.seq)
		if !ok {
			missing++
			continue
		}
		worst = max(worst, at.Sub(m.ackedAt))
	}
	return worst, missing
}

func newPair(t *testing.T) (user, user) {
	t.Helper()
	resetRateLimits(t)
	a, b := newUser(t), newUser(t)
	makeFriends(t, a, b)
	return a, b
}

// 杀掉 access：被杀实例上的客户端异常断开（1006），经网关重连到另一实例，补拉后不丢不重。
func TestFaultKillAccess(t *testing.T) {
	skipShort(t)
	a, b := newPair(t)
	sa := startSession(t, "a", a, nil)
	sb := startSession(t, "b", b, func(addr string) bool { return addr != sa.addr })
	victim := containerByIP(t, "access", hostOf(sb.addr))
	restoreService(t, victim, "access")

	sr := newSeries("kill-access")
	var killedAt time.Time
	for i := 1; i <= 25; i++ {
		if i == 6 {
			// 从发出 kill 命令起计时：命令返回时进程已经退出，之后再取时间会让断线早于计时起点
			killedAt = time.Now()
			docker(t, victim, "kill")
		}
		sr.send(t, sa, b.id, 30*time.Second)
		time.Sleep(sendInterval)
	}
	sb.waitConnected(t, 30*time.Second)
	afterRecovery(t, sr, sa, sb, b.id, 5)

	closes, outages := sb.snapshot()
	if len(closes) == 0 || len(outages) == 0 {
		t.Fatalf("b was not disconnected and reconnected: closes %v, outages %v", closes, outages)
	}
	if closes[0].code != 1006 {
		t.Errorf("b closed with %d, want 1006 (abnormal closure)", closes[0].code)
	}
	if c, _ := sa.snapshot(); len(c) != 0 {
		t.Errorf("a on the surviving instance was disconnected: %v", c)
	}
	verifyConversation(t, sa.conv.Load(), sr.n(), a.token, sb)
	pushed, dup, pulledOnly := sb.stats(sr.n())
	t.Logf("kill %s: b noticed after %v, reconnected after %v (incl. 0-2s jitter), pulled %d on reconnect; "+
		"b got %d/%d by push (%d duplicated, %d by pull only); send failures: %s",
		victim, closes[0].at.Sub(killedAt).Round(time.Millisecond), outages[0].up.Sub(outages[0].down).Round(time.Millisecond),
		outages[0].pulled, pushed, sr.n(), dup, pulledOnly, sr.failureSummary())
}

// 滚动重启 access：依次 docker restart -t 20 两个实例，前一个恢复 passing 后再重启下一个。
// 客户端收到 1001 而不是 1006，发送请求全部得到确认，不丢不重。
func TestFaultRollingRestartAccess(t *testing.T) {
	skipShort(t)
	a, b := newPair(t)
	sa := startSession(t, "a", a, nil)
	sb := startSession(t, "b", b, func(addr string) bool { return addr != sa.addr })
	order := []string{containerByIP(t, "access", hostOf(sb.addr)), containerByIP(t, "access", hostOf(sa.addr))}
	for _, c := range order {
		restoreService(t, c, "access")
	}

	type restart struct {
		name string
		took time.Duration
	}
	var restarts []restart
	done := make(chan error, 1)
	sr := newSeries("rolling-restart")
	sr.send(t, sa, b.id, 30*time.Second)
	go func() {
		for _, c := range order {
			start := time.Now()
			if out, err := dockerE(c, "restart", "-t", "20"); err != nil {
				done <- fmt.Errorf("restart %s: %v: %s", c, err, out)
				return
			}
			if err := waitPassingE("access", 2, 60*time.Second); err != nil {
				done <- err
				return
			}
			restarts = append(restarts, restart{c, time.Since(start)})
		}
		done <- nil
	}()
	for finished := false; !finished; {
		sr.send(t, sa, b.id, 60*time.Second)
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
			finished = true
		default:
		}
		time.Sleep(sendInterval)
	}
	sa.waitConnected(t, 30*time.Second)
	sb.waitConnected(t, 30*time.Second)
	afterRecovery(t, sr, sa, sb, b.id, 5)

	for _, s := range []*session{sa, sb} {
		closes, outages := s.snapshot()
		if len(closes) == 0 {
			t.Errorf("%s was never disconnected", s.name)
		}
		var downs []string
		for i, c := range closes {
			if c.code != 1001 {
				t.Errorf("%s closed with %d, want 1001 (going away)", s.name, c.code)
			}
			if i < len(outages) {
				downs = append(downs, fmt.Sprintf("%v (pulled %d)", outages[i].up.Sub(outages[i].down).Round(time.Millisecond), outages[i].pulled))
			}
		}
		t.Logf("%s: %d disconnects, offline %v", s.name, len(closes), downs)
	}
	verifyConversation(t, sa.conv.Load(), sr.n(), a.token, sb)
	pushed, dup, pulledOnly := sb.stats(sr.n())
	var took []string
	for _, r := range restarts {
		took = append(took, fmt.Sprintf("%s %v", r.name, r.took.Round(time.Millisecond)))
	}
	t.Logf("restarts [%s]; %d messages, b got %d by push (%d duplicated, %d by pull only); send failures: %s",
		strings.Join(took, ", "), sr.n(), pushed, dup, pulledOnly, sr.failureSummary())
}

// deliveryOwner 返回负责某个会话推送的 delivery 容器：按 Kafka 默认分区算法（murmur2）算出会话所在分区，
// 再从消费组的分配中找到持有该分区的实例。
func deliveryOwner(t *testing.T, conv int64) (string, int) {
	t.Helper()
	out := dockerExec(t, kafkaName, "/opt/kafka/bin/kafka-consumer-groups.sh",
		"--bootstrap-server", "kafka:29092", "--describe", "--group", "marschat-delivery")
	hosts := map[int]string{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) >= 8 && f[1] == kafka.TopicMessageEvents {
			if p, err := strconv.Atoi(f[2]); err == nil {
				hosts[p] = strings.TrimPrefix(f[7], "/")
			}
		}
	}
	if len(hosts) == 0 {
		t.Fatalf("no partition assignment of %s found:\n%s", kafka.TopicMessageEvents, out)
	}
	p := kgo.StickyKeyPartitioner(nil).ForTopic(kafka.TopicMessageEvents).
		Partition(&kgo.Record{Key: []byte(strconv.FormatInt(conv, 10))}, len(hosts))
	return containerByIP(t, "delivery", hosts[p]), p
}

// 杀掉 delivery：负责该会话分区的实例被杀后，消费组会话超时（10 秒）后分区转给另一个实例，推送恢复；
// 中断期间的消息延迟到达或经补拉拿到。
func TestFaultKillDelivery(t *testing.T) {
	skipShort(t)
	a, b := newPair(t)
	sa := startSession(t, "a", a, nil)
	sb := startSession(t, "b", b, nil)
	sr := newSeries("kill-delivery")
	first := sr.send(t, sa, b.id, 30*time.Second)
	sb.waitPushed(t, first.seq, 15*time.Second)
	victim, partition := deliveryOwner(t, sa.conv.Load())
	restoreService(t, victim, "delivery")

	killedAt := time.Now()
	docker(t, victim, "kill")
	for time.Since(killedAt) < 15*time.Second {
		sr.send(t, sa, b.id, 30*time.Second)
		time.Sleep(sendInterval)
	}
	afterRecovery(t, sr, sa, sb, b.id, 5)

	worst, missing := maxPushDelay(sb, sr.sent[1:])
	if worst < 3*time.Second {
		t.Errorf("max push delay %v after killing %s, want a visible stall (was partition %d really on it?)", worst, victim, partition)
	}
	verifyConversation(t, sa.conv.Load(), sr.n(), a.token, sb)
	pushed, dup, pulledOnly := sb.stats(sr.n())
	t.Logf("kill %s (partition %d): max push delay %v, %d not pushed; b got %d/%d by push (%d duplicated, %d by pull only); send failures: %s",
		victim, partition, worst.Round(time.Millisecond), missing, pushed, sr.n(), dup, pulledOnly, sr.failureSummary())
}

// 杀掉持有 Outbox 租约的 message：另一个实例在租约 TTL（10 秒）内接手，事件继续发布；
// 可能重复发布，被 delivery 的去重吸收。
func TestFaultKillOutboxLeader(t *testing.T) {
	skipShort(t)
	a, b := newPair(t)
	sa := startSession(t, "a", a, nil)
	sb := startSession(t, "b", b, nil)
	sr := newSeries("kill-outbox-leader")
	first := sr.send(t, sa, b.id, 30*time.Second)
	sb.waitPushed(t, first.seq, 15*time.Second)

	rdb := testkit.NewRedis(t)
	const leaseKey = "outbox:lease:marschat-message"
	owner := rdb.Get(context.Background(), leaseKey).Val()
	victim := leaseHolder(t, owner)
	restoreService(t, victim, "message")

	var takeover time.Duration
	var newOwner string
	watchDone := make(chan struct{})
	killedAt := time.Now()
	docker(t, victim, "kill")
	go func() {
		defer close(watchDone)
		for time.Since(killedAt) < 30*time.Second {
			if v, err := rdb.Get(context.Background(), leaseKey).Result(); err == nil && v != owner {
				takeover, newOwner = time.Since(killedAt), v
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
	}()
	for time.Since(killedAt) < 12*time.Second {
		sr.send(t, sa, b.id, 30*time.Second)
		time.Sleep(sendInterval)
	}
	<-watchDone
	if newOwner == "" {
		t.Fatalf("outbox lease not taken over within 30s after killing %s", victim)
	}
	afterRecovery(t, sr, sa, sb, b.id, 5)

	worst, missing := maxPushDelay(sb, sr.sent[1:])
	verifyConversation(t, sa.conv.Load(), sr.n(), a.token, sb)
	pushed, dup, pulledOnly := sb.stats(sr.n())
	if dup > 0 {
		t.Errorf("%d messages pushed more than once", dup)
	}
	t.Logf("kill %s (lease owner): lease taken over by %s after %v; max push delay %v, %d not pushed; "+
		"b got %d/%d by push (%d duplicated, %d by pull only); send failures: %s",
		victim, leaseHolder(t, newOwner), takeover.Round(time.Millisecond), worst.Round(time.Millisecond), missing,
		pushed, sr.n(), dup, pulledOnly, sr.failureSummary())
}

// 暂停 Kafka 30 秒：发送仍然成功（事件写入 Outbox），推送暂停；恢复后积压的事件按顺序发布并推送。
func TestFaultPauseKafka(t *testing.T) {
	skipShort(t)
	a, b := newPair(t)
	sa := startSession(t, "a", a, nil)
	sb := startSession(t, "b", b, nil)
	sr := newSeries("pause-kafka")
	first := sr.send(t, sa, b.id, 30*time.Second)
	sb.waitPushed(t, first.seq, 15*time.Second)

	t.Cleanup(func() { _, _ = dockerE(kafkaName, "unpause") })
	docker(t, kafkaName, "pause")
	pausedAt := time.Now()
	for time.Since(pausedAt) < 30*time.Second {
		sr.send(t, sa, b.id, 10*time.Second)
		time.Sleep(time.Second)
	}
	backlog := slices.Clone(sr.sent[1:])
	if len(sr.failures) > 0 {
		t.Errorf("sends failed while Kafka was paused: %s", sr.failureSummary())
	}
	if _, missing := maxPushDelay(sb, backlog); missing != len(backlog) {
		t.Errorf("%d of %d messages pushed while Kafka was paused", len(backlog)-missing, len(backlog))
	}

	docker(t, kafkaName, "unpause")
	unpausedAt := time.Now()
	waitHealthy(t, kafkaName, 60*time.Second)
	var caughtUp time.Time
	for _, m := range backlog {
		caughtUp = sb.waitPushed(t, m.seq, 90*time.Second)
	}
	sb.mu.Lock()
	var backlogOrder []int64
	for _, seq := range sb.order {
		if seq >= backlog[0].seq && seq <= backlog[len(backlog)-1].seq {
			backlogOrder = append(backlogOrder, seq)
		}
	}
	sb.mu.Unlock()
	if !slices.IsSorted(backlogOrder) {
		t.Errorf("backlog pushed out of order: %v", backlogOrder)
	}
	afterRecovery(t, sr, sa, sb, b.id, 5)
	verifyConversation(t, sa.conv.Load(), sr.n(), a.token, sb)
	pushed, dup, pulledOnly := sb.stats(sr.n())
	t.Logf("pause %s 30s: %d sends during the pause all acknowledged; backlog pushed in order, caught up %v after unpause; "+
		"b got %d/%d by push (%d duplicated, %d by pull only)",
		kafkaName, len(backlog), caughtUp.Sub(unpausedAt).Round(time.Millisecond), pushed, sr.n(), dup, pulledOnly)
}

// stopRedis 停掉 Redis，测试结束时（包括失败）确保它重新启动并健康。
func stopRedis(t *testing.T) time.Time {
	t.Helper()
	t.Cleanup(func() {
		if out, err := dockerE(redisName, "start"); err != nil {
			t.Errorf("restore redis: %v: %s", err, out)
			return
		}
		waitHealthy(t, redisName, 60*time.Second)
	})
	docker(t, redisName, "stop")
	return time.Now()
}

// 停掉 Redis 20 秒：发送成功（限流和吊销检查放行），推送停止；恢复后发布继续，客户端靠补拉拿到中断期间的消息。
func TestFaultStopRedis(t *testing.T) {
	skipShort(t)
	a, b := newPair(t)
	sa := startSession(t, "a", a, nil)
	sb := startSession(t, "b", b, nil)
	sr := newSeries("stop-redis")
	first := sr.send(t, sa, b.id, 30*time.Second)
	sb.waitPushed(t, first.seq, 15*time.Second)

	stoppedAt := stopRedis(t)
	for time.Since(stoppedAt) < 20*time.Second {
		sr.send(t, sa, b.id, 10*time.Second)
		time.Sleep(time.Second)
	}
	during := sr.n() - 1
	if len(sr.failures) > 0 {
		t.Errorf("sends failed while Redis was down for 20s: %s", sr.failureSummary())
	}
	docker(t, redisName, "start")
	waitHealthy(t, redisName, 60*time.Second)
	afterRecovery(t, sr, sa, sb, b.id, 5)
	verifyConversation(t, sa.conv.Load(), sr.n(), a.token, sb)
	pushed, dup, pulledOnly := sb.stats(sr.n())
	t.Logf("stop redis 20s: %d sends during the outage all acknowledged; b got %d/%d by push (%d duplicated, %d by pull only)",
		during, pushed, sr.n(), dup, pulledOnly)
}

// idProbe 是一次需要发号的写操作：给还没聊过的好友发第一条消息，message 要为新会话生成 ID。
type idProbe struct {
	at     time.Duration // 距 Redis 停机的时间
	ok     bool
	reason string
}

// 停掉 Redis 60 秒（TD-34）：已有会话的发送全程成功（不需要发号）；需要发号的写操作在节点号租约到期后失败，
// 到期时间取决于各实例最后一次续期，在停机后 20～30 秒之间；Redis 恢复后各服务换用新节点号，发号恢复。
func TestFaultStopRedisLong(t *testing.T) {
	skipShort(t)
	a, b := newPair(t)
	// 发号成功会建好会话，下一次探测要换一个新好友；失败的探测没有建会话，好友可以复用
	resetRateLimits(t)
	fresh := make([]user, 18)
	for i := range fresh {
		fresh[i] = newUser(t)
		makeFriends(t, fresh[i], a)
	}
	sa := startSession(t, "a", a, nil)
	sb := startSession(t, "b", b, nil)
	sr := newSeries("stop-redis-long")
	first := sr.send(t, sa, b.id, 30*time.Second)
	sb.waitPushed(t, first.seq, 15*time.Second)

	var probes []idProbe
	next := 0
	probe := func(stoppedAt time.Time) {
		if next == len(fresh) {
			t.Fatalf("ran out of fresh friends after %d probes", len(probes))
		}
		f, err := sa.send(fresh[next].id, fmt.Sprintf("%s-probe-%d", sr.prefix, len(probes)))
		if err != nil {
			// 连接问题，结果不确定（可能已经建了会话），换一个好友，不计入探测
			next++
			return
		}
		p := idProbe{at: time.Since(stoppedAt)}
		if f.GetSendAck() != nil {
			p.ok = true
			next++
		} else {
			p.reason = fmt.Sprintf("%d %s", f.GetError().GetCode(), f.GetError().GetReason())
		}
		probes = append(probes, p)
	}

	stoppedAt := stopRedis(t)
	restartCh := make(chan time.Time, 1)
	go func() {
		select {
		case <-t.Context().Done():
			// 测试已经失败退出，由 stopRedis 的清理函数启动 Redis
		case <-time.After(60 * time.Second):
			_, err := dockerE(redisName, "start")
			if err == nil {
				restartCh <- time.Now()
			}
		}
	}()

	var restartedAt, idRecoveredAt time.Time
	for i := 1; idRecoveredAt.IsZero(); i++ {
		sr.send(t, sa, b.id, 10*time.Second)
		select {
		case restartedAt = <-restartCh:
		default:
		}
		// 每两条发送探测一次：一轮 3 个请求，按轮询分到两个 message 实例时，探测轮流落在两个实例上
		if i%2 == 0 {
			probe(stoppedAt)
			if p := probes[len(probes)-1]; p.ok && !restartedAt.IsZero() && p.at > restartedAt.Sub(stoppedAt) {
				idRecoveredAt = stoppedAt.Add(p.at)
			}
		}
		if !restartedAt.IsZero() && time.Since(restartedAt) > 30*time.Second {
			t.Fatalf("writes that need an ID still failing 30s after Redis restarted; probes: %v", probes)
		}
		if time.Since(stoppedAt) > 90*time.Second && restartedAt.IsZero() {
			t.Fatal("Redis was not restarted after 60s")
		}
		time.Sleep(time.Second)
	}
	waitHealthy(t, redisName, 60*time.Second)

	// 租约最早在停机 20 秒后到期，最晚 30 秒（加 1 秒余量：续期请求在停机前发出但还没返回）
	var firstFail, lastOK time.Duration
	failReasons := map[string]int{}
	down := restartedAt.Sub(stoppedAt)
	for _, p := range probes {
		if p.at >= down {
			break
		}
		switch {
		case p.ok && p.at > 31*time.Second:
			t.Errorf("write that needs an ID succeeded %v after Redis stopped, the ID lease should have expired", p.at)
		case !p.ok && p.at < 19*time.Second:
			t.Errorf("write that needs an ID failed %v after Redis stopped (%s), the ID lease was still valid", p.at, p.reason)
		}
		if p.ok {
			lastOK = p.at
		} else {
			if firstFail == 0 {
				firstFail = p.at
			}
			failReasons[p.reason]++
		}
	}
	if firstFail == 0 {
		t.Error("no write that needs an ID failed during a 60s Redis outage, expected the ID lease to expire")
	}
	// 租约失效是可恢复的依赖故障，应返回 503 让客户端稍后重试，而不是 500
	for reason := range failReasons {
		if reason != "503 DEPENDENCY_UNAVAILABLE" {
			t.Errorf("writes that need an ID failed with %s while the ID lease was lost, want 503 DEPENDENCY_UNAVAILABLE", reason)
		}
	}
	if len(sr.failures) > 0 {
		t.Errorf("sends in an existing conversation failed while Redis was down: %s", sr.failureSummary())
	}

	// 停机 60 秒超过租约 TTL，Redis 恢复时所有节点号键都已过期，每个实例都要换一次节点号
	since := stoppedAt.UTC().Format(time.RFC3339)
	var switches, refused []string
	for _, c := range []string{"marschat-message-1", "marschat-message-2", "marschat-account-1", "marschat-social-1"} {
		logs, _ := dockerE(c, "logs", "--since", since)
		n := strings.Count(logs, "idgen node lease lost, switched to a new node")
		if n == 0 {
			t.Errorf("%s did not switch to a new ID node after the outage", c)
		}
		switches = append(switches, fmt.Sprintf("%s=%d", c, n))
		if strings.HasPrefix(c, "marschat-message") {
			refused = append(refused, fmt.Sprintf("%s=%d", c, strings.Count(logs, `"reason":"DEPENDENCY_UNAVAILABLE"`)))
		}
	}
	// account（注册）和 social（好友申请）也要恢复发号
	resetRateLimits(t)
	c, d := newUser(t), newUser(t)
	makeFriends(t, c, d)

	afterRecovery(t, sr, sa, sb, b.id, 5)
	verifyConversation(t, sa.conv.Load(), sr.n(), a.token, sb)
	pushed, dup, pulledOnly := sb.stats(sr.n())
	t.Logf("stop redis 60s: %d sends in the existing conversation, failures: %s; writes that need an ID: "+
		"last success %v, first failure %v after stop (%v), refused per instance %v, recovered %v after Redis started; "+
		"node switches %v; b got %d/%d by push (%d duplicated, %d by pull only)",
		sr.n(), sr.failureSummary(), lastOK.Round(time.Millisecond), firstFail.Round(time.Millisecond), failReasons,
		refused, idRecoveredAt.Sub(restartedAt).Round(time.Millisecond), switches, pushed, sr.n(), dup, pulledOnly)
}
