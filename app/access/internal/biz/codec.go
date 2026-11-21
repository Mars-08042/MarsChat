package biz

import (
	"github.com/go-kratos/kratos/v3/encoding"
	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"

	v1 "github.com/Mars-08042/MarsChat/api/access/v1"
	"github.com/Mars-08042/MarsChat/pkg/jsoncodec"
)

// 子协议决定帧的编码，客户端在握手时通过 Sec-WebSocket-Protocol 选择，不选时用 protobuf。
const (
	SubprotocolProto = "marschat.v1.proto"
	SubprotocolJSON  = "marschat.v1.json"
)

// Subprotocols 是服务端支持的子协议，按优先级排列。
var Subprotocols = []string{SubprotocolProto, SubprotocolJSON}

// codec 负责一条连接上帧的编解码。
type codec struct {
	// WebSocket 消息类型：protobuf 用二进制消息，JSON 用文本消息
	msgType   int
	marshal   func(*v1.ServerFrame) ([]byte, error)
	unmarshal func([]byte, *v1.ClientFrame) error
}

var protoCodec = codec{
	msgType:   websocket.BinaryMessage,
	marshal:   func(f *v1.ServerFrame) ([]byte, error) { return proto.Marshal(f) },
	unmarshal: func(b []byte, f *v1.ClientFrame) error { return proto.Unmarshal(b, f) },
}

// JSON 与 HTTP 接口使用同一个 codec（pkg/jsoncodec）：int64 为字符串、枚举为名字、零值字段也输出。
var jsonCodec = codec{
	msgType:   websocket.TextMessage,
	marshal:   func(f *v1.ServerFrame) ([]byte, error) { return encoding.GetCodec(jsoncodec.Name).Marshal(f) },
	unmarshal: func(b []byte, f *v1.ClientFrame) error { return encoding.GetCodec(jsoncodec.Name).Unmarshal(b, f) },
}

func codecFor(subprotocol string) codec {
	if subprotocol == SubprotocolJSON {
		return jsonCodec
	}
	return protoCodec
}
