// Package jsoncodec 替换 Kratos 注册的 json codec：proto 消息按 protobuf JSON 规则编解码，
// 其他值仍用标准库。使用方只需匿名导入（pkg/bootstrap 已导入）：
//
//	import _ "github.com/Mars-08042/MarsChat/pkg/jsoncodec"
//
// 为什么需要它：Kratos v3 把 json codec 改成了纯标准库实现（见 Kratos 的 docs/migration/v2-to-v3），
// HTTP 接口收发 proto 消息时，枚举只能写数字、int64 不是字符串、Timestamp 编码成 {seconds, nanos}，
// 字段名也变成 proto 原名，与 gRPC-Gateway、OpenAPI 文档约定的 protobuf JSON 格式不一致。
// Kratos 提供了 contrib 兼容包，这里自己实现，不为几十行代码引入新依赖。
//
// 覆盖注册依赖初始化顺序：本包导入了 Kratos 的 encoding/json，它一定先于本包初始化，
// 所以本包的注册会覆盖它。
package jsoncodec

import (
	"encoding/json"

	"github.com/go-kratos/kratos/v3/encoding"
	_ "github.com/go-kratos/kratos/v3/encoding/json"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// Name 与 Kratos 的 json codec 同名，HTTP 按 Content-Type 的子类型 json 选中它。
const Name = "json"

var (
	// 输出零值字段，客户端不用区分“字段缺失”和“零值”
	marshalOptions = protojson.MarshalOptions{EmitUnpopulated: true}
	// 忽略未知字段，客户端先于服务端升级时不会报错
	unmarshalOptions = protojson.UnmarshalOptions{DiscardUnknown: true}
)

func init() {
	encoding.RegisterCodec(codec{})
}

type codec struct{}

func (codec) Marshal(v any) ([]byte, error) {
	if m, ok := v.(proto.Message); ok {
		return marshalOptions.Marshal(m)
	}
	return json.Marshal(v)
}

func (codec) Unmarshal(data []byte, v any) error {
	if len(data) == 0 {
		return nil
	}
	if m, ok := v.(proto.Message); ok {
		return unmarshalOptions.Unmarshal(data, m)
	}
	return json.Unmarshal(data, v)
}

func (codec) Name() string { return Name }
