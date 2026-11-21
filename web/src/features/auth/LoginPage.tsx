import { useState, type FormEvent } from "react"
import { Link, useNavigate } from "react-router"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { looksLikeEmail } from "./email"
import { EmailCodeField, Field, PasswordInput, SubmitButton, TextInput } from "./fields"

type Method = "password" | "code"

export function LoginPage() {
  const navigate = useNavigate()
  const [method, setMethod] = useState<Method>("password")
  const [email, setEmail] = useState("")
  const [password, setPassword] = useState("")
  const [code, setCode] = useState("")
  const [submitting, setSubmitting] = useState(false)

  const ready = looksLikeEmail(email) && (method === "password" ? password !== "" : code.length === 6)

  const submit = (e: FormEvent) => {
    e.preventDefault()
    if (!ready) return
    setSubmitting(true)
    // 原型阶段直接进入聊天页；第 3 阶段接入 LoginByPassword / LoginByEmailCode
    setTimeout(() => navigate("/chat"), 700)
  }

  return (
    <div className="space-y-8">
      <div className="space-y-2">
        <h1 className="font-display text-3xl font-semibold tracking-tight">欢迎回来</h1>
        <p className="text-sm text-muted-foreground">登录 MarsChat，继续没聊完的话题</p>
      </div>

      <Tabs value={method} onValueChange={(value) => setMethod(value as Method)} className="gap-6">
        <TabsList className="h-10 w-full group-data-horizontal/tabs:h-10">
          <TabsTrigger value="password">密码登录</TabsTrigger>
          <TabsTrigger value="code">验证码登录</TabsTrigger>
        </TabsList>

        <form onSubmit={submit} className="space-y-5" noValidate>
          <Field label="邮箱" htmlFor="email">
            <TextInput
              id="email"
              type="email"
              autoComplete="email"
              placeholder="you@example.com"
              value={email}
              onChange={(e) => setEmail(e.target.value.trim())}
            />
          </Field>

          <TabsContent value="password">
            <Field label="密码" htmlFor="password">
              <PasswordInput
                id="password"
                autoComplete="current-password"
                placeholder="输入密码"
                maxLength={64}
                value={password}
                onChange={(e) => setPassword(e.target.value)}
              />
            </Field>
          </TabsContent>
          <TabsContent value="code">
            <EmailCodeField email={email} value={code} onChange={setCode} />
          </TabsContent>

          <SubmitButton loading={submitting} disabled={!ready}>
            登录
          </SubmitButton>
        </form>
      </Tabs>

      <p className="text-center text-sm text-muted-foreground">
        还没有账号？
        <Link to="/register" className="font-medium text-brand-text underline-offset-4 hover:underline">
          注册
        </Link>
      </p>
    </div>
  )
}
