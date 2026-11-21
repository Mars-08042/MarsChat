/// <reference types="vitest/config" />
import { createReadStream, readFileSync } from "node:fs"
import { fileURLToPath } from "node:url"
import tailwindcss from "@tailwindcss/vite"
import react from "@vitejs/plugin-react"
import { defineConfig, type Plugin, type ProxyOptions } from "vite"

// 表情数据自托管（设计 3.6.4）：frimousse 按 `${emojibaseUrl}/${locale}/data.json` 和 messages.json 取数据。
// 开发时由中间件直接读 node_modules，构建时复制到 dist/emojibase，运行时不访问第三方 CDN。
const EMOJIBASE_FILES = ["zh/data.json", "zh/messages.json"]

function emojibaseData(): Plugin {
  const source = (file: string) => fileURLToPath(new URL(`./node_modules/emojibase-data/${file}`, import.meta.url))
  return {
    name: "marschat-emojibase-data",
    configureServer(server) {
      server.middlewares.use("/emojibase", (req, res, next) => {
        const file = (req.url ?? "").split("?")[0].replace(/^\//, "")
        if (!EMOJIBASE_FILES.includes(file)) return next()
        res.setHeader("Content-Type", "application/json; charset=utf-8")
        createReadStream(source(file)).pipe(res)
      })
    },
    generateBundle() {
      for (const file of EMOJIBASE_FILES) {
        this.emitFile({ type: "asset", fileName: `emojibase/${file}`, source: readFileSync(source(file)) })
      }
    },
  }
}

// 开发代理：浏览器只访问 Vite 的源，网关和各服务都没有配置 CORS（设计 3.10）。
// MARSCHAT_API_TARGET=gateway（默认）转发到容器模式的 APISIX；
// MARSCHAT_API_TARGET=host 按路径前缀转发到宿主机上运行的各个服务（make run-<服务>）。
const GATEWAY = "http://127.0.0.1:19080"

// 宿主机模式的路由与 deploy/apisix/apisix.yaml 的路径划分保持一致。
// Vite 按前缀匹配，"/v1/friends" 不会误匹配 "/v1/friend-requests"。
const HOST_ROUTES: Record<string, string> = {
  "/v1/auth": "http://127.0.0.1:18010",
  "/v1/users": "http://127.0.0.1:18010",
  "/v1/sessions": "http://127.0.0.1:18010",
  "/v1/admin": "http://127.0.0.1:18010",
  "/v1/friend-requests": "http://127.0.0.1:18020",
  "/v1/friends": "http://127.0.0.1:18020",
  "/v1/blocks": "http://127.0.0.1:18020",
  "/v1/groups": "http://127.0.0.1:18020",
  "/v1/group-join-requests": "http://127.0.0.1:18020",
  "/v1/conversations": "http://127.0.0.1:18030",
  "/v1/files": "http://127.0.0.1:18050",
}

function apiProxy(): Record<string, ProxyOptions> {
  const mode = process.env.MARSCHAT_API_TARGET ?? "gateway"
  if (mode === "host") {
    const routes = Object.fromEntries(
      Object.entries(HOST_ROUTES).map(([prefix, target]) => [prefix, { target }]),
    )
    return { ...routes, "/ws": { target: "ws://127.0.0.1:18000", ws: true } }
  }
  if (mode !== "gateway") {
    throw new Error(`MARSCHAT_API_TARGET must be "gateway" or "host", got "${mode}"`)
  }
  return {
    "/v1": { target: GATEWAY },
    "/ws": { target: GATEWAY, ws: true },
  }
}

export default defineConfig({
  plugins: [react(), tailwindcss(), emojibaseData()],
  resolve: {
    alias: {
      "@": fileURLToPath(new URL("./src", import.meta.url)),
    },
  },
  server: {
    port: 5173,
    strictPort: true,
    proxy: apiProxy(),
  },
  test: {
    include: ["src/**/*.test.ts"],
    // Vitest 默认把 CSS 替换为空字符串；tokens.test.ts 要读取 tokens.css 原文核对对比度
    css: { include: [/styles\/tokens\.css/] },
  },
})
