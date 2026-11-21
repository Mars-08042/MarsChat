package kafka

import (
	"errors"
	"testing"

	"github.com/twmb/franz-go/pkg/kadm"
)

// 按消费组和 topic 汇总分区积压，出错的分区和消费组跳过，整个 topic 都出错时不输出。
func TestSumLags(t *testing.T) {
	described := kadm.DescribedGroupLags{
		"marschat-delivery": {
			Group: "marschat-delivery",
			Lag: kadm.GroupLag{
				"marschat.message.events": {
					0: {Lag: 3},
					1: {Lag: 0},
					2: {Lag: -1, Err: errors.New("list offsets failed")},
				},
				"marschat.account.events": {
					0: {Lag: -1, Err: errors.New("commit fetch failed")},
				},
			},
		},
		"marschat-social-cache": {
			Group:       "marschat-social-cache",
			DescribeErr: errors.New("coordinator not available"),
			Lag:         kadm.GroupLag{"marschat.social.events": {0: {Lag: 9}}},
		},
	}
	got := sumLags(described)
	want := map[groupTopic]int64{{group: "marschat-delivery", topic: "marschat.message.events"}: 3}
	if len(got) != len(want) {
		t.Fatalf("sumLags = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("lag[%v] = %d, want %d", k, got[k], v)
		}
	}
}
