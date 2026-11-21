package jsoncodec_test

import (
	"strings"
	"testing"
	"time"

	"github.com/go-kratos/kratos/v3/encoding"
	"github.com/go-kratos/kratos/v3/errors"
	"google.golang.org/protobuf/types/known/timestamppb"

	v1 "github.com/Mars-08042/MarsChat/api/account/v1"
	_ "github.com/Mars-08042/MarsChat/pkg/jsoncodec"
)

func TestProtoMessageUsesProtoJSON(t *testing.T) {
	c := encoding.GetCodec("json")
	var req v1.SendEmailCodeRequest
	if err := c.Unmarshal([]byte(`{"email":"user@example.com","purpose":"CODE_PURPOSE_LOGIN","unknown":1}`), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if req.GetPurpose() != v1.CodePurpose_CODE_PURPOSE_LOGIN {
		t.Fatalf("purpose = %v", req.GetPurpose())
	}

	out, err := c.Marshal(&v1.User{Id: 1 << 60, CreatedAt: timestamppb.New(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, want := range []string{`"id":"1152921504606846976"`, `"createdAt":"2026-01-02T03:04:05Z"`, `"role":"ROLE_UNSPECIFIED"`, `"nickname":""`} {
		if !strings.Contains(string(out), want) {
			t.Errorf("output %s missing %s", out, want)
		}
	}
}

func TestErrorAndPlainValues(t *testing.T) {
	c := encoding.GetCodec("json")
	out, err := c.Marshal(errors.BadRequest("VALIDATOR", "bad"))
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}
	if !strings.Contains(string(out), `"reason":"VALIDATOR"`) || !strings.Contains(string(out), `"code":400`) {
		t.Errorf("error output = %s", out)
	}

	var m map[string]any
	if err := c.Unmarshal([]byte(`{"a":1}`), &m); err != nil || m["a"] != float64(1) {
		t.Errorf("plain unmarshal = %v, %v", m, err)
	}
}
