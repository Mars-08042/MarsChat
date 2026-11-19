package server

import "github.com/google/wire"

// ProviderSet 是 server 层的依赖提供者。
var ProviderSet = wire.NewSet(NewGRPCServer, NewHTTPServer)
