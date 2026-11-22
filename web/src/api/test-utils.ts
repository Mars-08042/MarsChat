// api/ 单元测试共用的假依赖，不被业务代码引用。

import type { KeyValueStorage, TokenGrant } from "./tokens"

export function memoryStorage(init: Record<string, string> = {}): KeyValueStorage & { data: Map<string, string> } {
  const data = new Map(Object.entries(init))
  return {
    data,
    getItem: (k) => data.get(k) ?? null,
    setItem: (k, v) => void data.set(k, v),
    removeItem: (k) => void data.delete(k),
  }
}

// 模拟 Web Locks：同一把锁上的任务按调用顺序依次执行，多个令牌管理实例共用一把锁就相当于多个标签页
export function createLock(): <T>(fn: () => Promise<T>) => Promise<T> {
  let tail: Promise<unknown> = Promise.resolve()
  return <T>(fn: () => Promise<T>) => {
    const run = tail.then(fn)
    tail = run.catch(() => undefined)
    return run
  }
}

export function grant(n: number, accessExpiresIn = 900, refreshExpiresIn = 30 * 86400): TokenGrant {
  return { accessToken: `a${n}`, accessExpiresIn, refreshToken: `r${n}`, refreshExpiresIn }
}
